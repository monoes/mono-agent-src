package account

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sessionFile = "session.json"
	refreshFile = "refresh.enc"
	lockFile    = "session.lock"
)

// errSessionInvalid is in every Load failure that means session.json is there and
// cannot be used as it is: it does not parse, it is of a version this build does
// not read, it is not a regular file, or it is larger than the store reads
// (readStoreFile). A failure to open or to read the file (permission denied, an
// I/O error) is not: it says nothing about what the file holds, so a caller can
// tell a file that is unusable from a disk that failed. errors.Is finds it; the
// message stays the failure's own. The line is whether the open succeeded: a FIFO,
// a device or a directory opens and is found not to be a regular file, so that is
// the file's content; a socket, a link loop (ELOOP), a link to a terminal (ENXIO)
// or a file that may not be opened fail the open or the read, and are not. Whoever
// can write the account folder can therefore force an I/O failure at will. A guard
// that keeps its cached session on one is bounded by that session's own expiry and
// grace, and a new process judges the same file locked(invalid).
var errSessionInvalid = errors.New("account: " + sessionFile + " is not valid")

// invalidSession marks a Load failure as errSessionInvalid and keeps its message.
type invalidSession struct{ error }

func (e invalidSession) Unwrap() []error { return []error{e.error, errSessionInvalid} }

// Store is the on-disk session. Nothing creates a file or a directory until a
// write: every read of a missing session answers "none". The mutating methods
// (Save, SaveRefresh, DeleteRefresh) are safe across processes only under Lock,
// and LoadRefresh is called under it too: the key store calls of the two are kept
// to at most one parked at a time only because their callers take turns
// (keyStoreLimit).
type Store interface {
	Load() (*Session, error)                             // nil, nil when there is no session.json
	Save(*Session) error                                 // atomic, 0600
	LoadRefresh() (string, error)                        // "", nil when none; ErrKeyringUnavailable when the key store cannot be opened, does not answer in time or still waits for an earlier call
	SaveRefresh(token string) error                      // seals it; the directory is created here if needed; ErrKeyringUnavailable as LoadRefresh
	DeleteRefresh() error                                // nil when there is none
	Lock(ctx context.Context) (unlock func(), err error) // exclusive, cross-process
	Mtime() (time.Time, error)                           // of session.json; zero time when absent
	Dir() string
}

// keyStoreTimeout is how long the store waits for one call into the key store
// (Open or Seal of the refresh token) before it gives up on it. Both calls are
// made while session.lock is held, the second one after monoes.me has rotated
// the refresh token, and the OS key stores wait without bound for a locked
// keychain or an unlock prompt nobody answers: one waiting key store would hold
// the lock against every process of the machine, stop every refresh and every
// sign-in, and keep Close and Ctrl-C waiting. The interactive sealer of the
// sign-in, which waits for a person, is exempt (callKeyStore). A var so that a
// test can shorten it.
var keyStoreTimeout = 10 * time.Second

// keyStoreTimer starts the wait for one key store call: its channel receives when d has
// passed, and stop ends it. It is a variable so that a test fires the timeout itself (the
// real one is the time of the machine under test, which a loaded runner stretches).
var keyStoreTimer = func(d time.Duration) (expired <-chan time.Time, stop func()) {
	timer := time.NewTimer(d)
	return timer.C, func() { timer.Stop() }
}

// keyStoreResult is what one call into the key store answered.
type keyStoreResult struct {
	data []byte
	err  error
}

// keyStoreLimit counts the bounded calls into the key store that were given up
// on and have not returned yet: parked, a goroutine each and, with go-keyring on
// macOS, a `security` child process each. A key store that never answers (an
// unlock dialog nobody sees) is asked again by every attempt, by a daemon every 30
// s to 5 min, and without a limit each attempt that timed out would leave one more
// call parked until someone answers. So while one is parked callKeyStore starts no
// other bounded call: it fails at once with an error that is ErrKeyringUnavailable,
// which the guard records as keyring_unavailable as it records a timeout.
//
// The limit keeps at most one call parked only because every bounded call is made
// with session.lock held: the lock is exclusive between the callers of one
// process too (flock is per open file), so they take turns, and a call that finds
// one parked is refused. Two callers that start at once without it both pass the
// check, both time out and both park. So a bounded LoadRefresh or SaveRefresh must
// be called with the lock held: the guard's refresh does, and so must any other
// caller of the quiet sealer (a logout, an adoption of an older login).
type keyStoreLimit struct{ parked atomic.Int32 }

// processKeyStoreLimit is the limit every store of this process shares: they all
// ask the same key store, so a call that one left parked refuses the calls of the
// others. A test gives a store a limit of its own to stand for another process.
var processKeyStoreLimit = &keyStoreLimit{}

// callKeyStore runs fn, one call into the key store through sealer s, and waits
// for its result for at most keyStoreTimeout. When the wait ends first it returns
// an error that is ErrKeyringUnavailable: the key store is not usable now, which
// is no decision about the account, so the guard keeps the session's grace. The
// call itself cannot be cancelled: it goes on in its goroutine until the key
// store answers, and then ends, because the channel is buffered and its result is
// dropped. Only the call runs there: what the caller does with a result, writing
// refresh.enc, happens on the caller's goroutine and only for a result that came
// in time, so a call that was given up on can never write refresh.enc. (A Seal
// that was given up on can still reach the key store and make the first key: see
// accountKEKVault in internal/secrets.)
//
// A call that was given up on is parked in l until it returns, and while one is
// parked no other bounded call starts: it fails at once, saying that the key store
// is still waiting for an earlier request (see keyStoreLimit).
//
// A sealer that may wait for a person (the interactive keyring sealer, see
// isPrompting) is neither bounded nor counted: fn runs on the caller's goroutine
// and is waited for. A person types the passphrase and answers the unlock dialog,
// which takes as long as it takes; the explicit sign-in owns the terminal and
// holds the lock meanwhile, and the refreshes of other processes give up after
// lockWaitTimeout and record nothing while the prompt is open (see
// NewInteractiveKeyringSealer).
func callKeyStore(l *keyStoreLimit, s Sealer, fn func() ([]byte, error)) ([]byte, error) {
	if isPrompting(s) {
		return fn()
	}
	// The caller holds session.lock, so no other bounded call of this process runs
	// between this check and the parking below: at most one call is ever parked
	// (see keyStoreLimit).
	if l.parked.Load() > 0 {
		return nil, fmt.Errorf("%w: the key store is still waiting for an earlier request", ErrKeyringUnavailable)
	}
	const (
		callRunning  = iota // the caller waits for the call
		callParked          // the caller gave up on the call, which is counted until it returns
		callReturned        // the call returned
	)
	var state atomic.Int32
	done := make(chan keyStoreResult, 1)
	go func() {
		data, err := fn()
		done <- keyStoreResult{data, err}
		if !state.CompareAndSwap(callRunning, callReturned) {
			l.parked.Add(-1) // the caller had given up on it: the key store has answered at last
		}
	}()
	timeout := keyStoreTimeout
	expired, stop := keyStoreTimer(timeout)
	defer stop()
	select {
	case r := <-done:
		return r.data, r.err
	case <-expired:
		l.parked.Add(1)
		if state.CompareAndSwap(callRunning, callParked) {
			return nil, fmt.Errorf("%w: the key store did not answer within %v", ErrKeyringUnavailable, timeout)
		}
		l.parked.Add(-1) // the call returned as the timer fired: its result is in the channel
		r := <-done
		return r.data, r.err
	}
}

// lockPause is the wait between two attempts to take session.lock. It is a variable so that
// a test reads the schedule of the waits and makes them pass at once.
var lockPause = time.After

// DefaultDir is ~/.monoagent/account, found through os.UserHomeDir. The path
// is the same whatever --db-path says: there is one session per OS user.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("account: finding the home directory: %w", err)
	}
	return filepath.Join(home, ".monoagent", "account"), nil
}

type fileStore struct {
	dir    string
	sealer Sealer
	limit  *keyStoreLimit // the parked key store calls this store counts; the process's, unless a test says otherwise
	err    error          // the default directory could not be resolved: no session can exist, and none can be written
}

// OpenStore opens the session in dir (the default directory when dir is "")
// with s as the refresh-token sealer (the production keyring sealer when s is
// nil). It touches nothing on disk.
func OpenStore(dir string, s Sealer) Store {
	st := &fileStore{dir: dir, sealer: s, limit: processKeyStoreLimit}
	if dir == "" {
		st.dir, st.err = DefaultDir()
	}
	if s == nil {
		st.sealer = NewKeyringSealer()
	}
	return st
}

func (s *fileStore) Dir() string { return s.dir }

func (s *fileStore) path(name string) string { return filepath.Join(s.dir, name) }

func (s *fileStore) Load() (*Session, error) {
	if s.err != nil {
		return nil, nil
	}
	data, err := readStoreFile(s.path(sessionFile))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil
	case errors.Is(err, errNotRegular), errors.Is(err, errTooLarge):
		return nil, invalidSession{err}
	case err != nil:
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, invalidSession{fmt.Errorf("account: %s is not valid: %w", sessionFile, err)}
	}
	if sess.V != sessionVersion {
		return nil, invalidSession{fmt.Errorf("account: %s has version %d, this build reads version %d", sessionFile, sess.V, sessionVersion)}
	}
	return &sess, nil
}

func (s *fileStore) Save(sess *Session) error {
	if s.err != nil {
		return s.err
	}
	if sess == nil {
		return errors.New("account: no session to save")
	}
	cp := *sess
	cp.V = sessionVersion
	data, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path(sessionFile), append(data, '\n'))
}

func (s *fileStore) Mtime() (time.Time, error) {
	if s.err != nil {
		return time.Time{}, nil
	}
	fi, err := os.Stat(s.path(sessionFile))
	if errors.Is(err, os.ErrNotExist) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// LoadRefresh unseals the refresh token. A missing refresh.enc is "", nil and
// never reaches the key store, so reading a machine with no session cannot
// raise a keychain prompt. A key store that does not answer within
// keyStoreTimeout, or that still waits for an earlier call that timed out, is
// ErrKeyringUnavailable (callKeyStore), and so is a token sealed for another
// folder: this one is a copy (sealedFor).
func (s *fileStore) LoadRefresh() (string, error) {
	if s.err != nil {
		return "", nil
	}
	sealed, err := readStoreFile(s.path(refreshFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	plain, err := callKeyStore(s.limit, s.sealer, func() ([]byte, error) { return s.sealer.Open(sealed) })
	if err != nil {
		return "", err
	}
	return sealedFor(s.dir, plain)
}

// SaveRefresh seals the refresh token for this folder (sealFor) and writes
// refresh.enc. Its callers hold the store lock, whose file is in the folder, so
// the folder exists and has an identity to seal. A key store that does not
// answer within keyStoreTimeout, or that still waits for an earlier call that
// timed out, is ErrKeyringUnavailable, and nothing is written.
func (s *fileStore) SaveRefresh(token string) error {
	if s.err != nil {
		return s.err
	}
	if token == "" {
		return errors.New("account: empty refresh token")
	}
	sealed, err := callKeyStore(s.limit, s.sealer, func() ([]byte, error) { return s.sealer.Seal(sealFor(s.dir, token)) })
	if err != nil {
		return err
	}
	return writeFileAtomic(s.path(refreshFile), sealed)
}

func (s *fileStore) DeleteRefresh() error {
	if s.err != nil {
		return nil
	}
	err := os.Remove(s.path(refreshFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// Made durable at once: a remove that a power cut undoes brings back a refresh
	// token that was rotated away, and the next refresh would present it.
	syncDirFn(s.dir)
	return nil
}

// syncDirFn is syncDir, a variable so that a test can see when it is called.
var syncDirFn = syncDir

// errLockHeld is what tryLock returns while another holder has the lock.
var errLockHeld = errors.New("account: lock is held")

// Lock takes the exclusive cross-process lock on session.lock, waiting until
// ctx ends. The lock file and the directory are created here, so Lock is only
// for a caller that is about to write. unlock is safe to call twice. The OS
// drops the lock if the process dies, so a crash leaves nothing stale.
func (s *fileStore) Lock(ctx context.Context) (func(), error) {
	if s.err != nil {
		return nil, s.err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.path(lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for delay := 5 * time.Millisecond; ; {
		err := tryLock(f)
		if err == nil {
			break
		}
		if !errors.Is(err, errLockHeld) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-lockPause(delay):
		}
		delay = min(delay*2, 50*time.Millisecond)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			unlock(f)
			f.Close()
		})
	}, nil
}

// writeFileAtomic writes data to path (mode 0600, directory 0700 created on
// the first write) through a temporary file in the same directory and a rename,
// so a reader sees the old file or the new one, never half of one. It then syncs
// the directory, so that a power cut cannot bring the old file back (Windows has
// no directory flush: its rename is written through instead, replaceFile). Every
// write of session.json and refresh.enc goes through here. A crash
// between creating the temporary file and the rename leaves a 0600 ".tmp-*" file
// that nothing reads or removes.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op once the rename has moved it
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameReplacingFn(tmp.Name(), path); err != nil {
		return err
	}
	syncDirFn(dir)
	return nil
}

// renameReplacingFn is renameReplacing, a variable so that a test can see every
// write go through it.
var renameReplacingFn = renameReplacing

// renameReplacing renames over an existing file through replaceFile, which on
// Windows writes the rename through. On Windows the rename can fail while another
// process has the target open (a reader of the store does not share delete
// access), so it is retried briefly: a lost write here could lose a rotated
// refresh token.
func renameReplacing(from, to string) error {
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = 5
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = replaceFile(from, to); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}
