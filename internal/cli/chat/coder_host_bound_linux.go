//go:build linux

package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/procbind"
)

// A turn launched with peer-credential binding on is started and handed its credentials in this order (design 3c):
//
//  1. the process is started with a held pidfd, with NO token in its environment or arguments, and the read end of a
//     frame pipe (the host keeps the write end, close-on-exec, so no other turn can inherit it);
//  2. its start stamp is read and it is confirmed alive;
//  3. the token is registered, pending, with a handle of its own (a dup of the pidfd);
//  4. ONE write transaction re-reads that the session's current turn is this turn and inserts the token and its
//     binding (worker pid, stamp, host generation);
//  5. the revocation row is read, the entry is activated, and only then is the frame written and the pipe closed.
//
// A failure before step 5 kills the turn through the pidfd, unregisters the entry and revokes whatever was inserted:
// nothing was delivered. The turn itself waits for the frame (10 s) and ends if it does not come.

// boundHost is what the bound launch needs of the host's binding services.
type boundHost interface {
	Ensure(ctx context.Context, profile string) error
	Generation(profile string) (string, bool)
}

// boundLaunch is the configuration of the bound launch; a launcher without one launches exactly as it always did.
type boundLaunch struct {
	db     *sql.DB
	host   boundHost
	binder *procbind.Binder // the registry the host's services answer from
	// beforeMint and afterMint are test seams: they run just before the transaction of step 4 and between it and the
	// revocation read of step 5.
	beforeMint func()
	afterMint  func()
	// beforeKill and afterKill are test seams: they run just before and right after the leader is signalled on a failed delivery.
	beforeKill func()
	afterKill  func()
}

// newBoundLaunch is the one way a launcher gets a bound launch: it also points the stores that end tokens at the same
// registry (store: the session tokens, child: the task tokens), so every revocation of a token ends its live entry
// after it commits and no registration outlives its token.
func newBoundLaunch(db *sql.DB, host boundHost, binder *procbind.Binder, store *coderhost.Store, child *childTokenMinter) *boundLaunch {
	if store != nil {
		store.SetBinder(binder)
	}
	if child != nil && child.tokens != nil {
		child.tokens.SetBinder(binder)
	}
	return &boundLaunch{db: db, host: host, binder: binder}
}

// boundDelivery is one turn's frame pipe, pidfd and registration.
type boundDelivery struct {
	b            *boundLaunch
	r, w         *os.File
	pidfd        int
	missionToken string
	// child, when set, is the mission child whose task token this turn also gets (a v2 mission token, owner = the run).
	child *childToken
	// onKill, when set, is called before the leader is killed (the launcher marks the turn as stopping, so its group
	// is killed with the leader's exit, before the reap).
	onKill func()
}

// attach prepares cmd for a bound launch: the frame pipe on descriptor 3 and its marker, nothing else.
func (b *boundLaunch) attach(cmd *exec.Cmd, missionToken string) (*boundDelivery, error) {
	r, w, err := os.Pipe() // close-on-exec both ends; the child gets r only through ExtraFiles
	if err != nil {
		return nil, err
	}
	if len(cmd.ExtraFiles) != 0 {
		r.Close()
		w.Close()
		return nil, errors.New("extra files already set")
	}
	cmd.ExtraFiles = []*os.File{r}
	cmd.Env = append(cmd.Env, coderhost.CredFDEnv+"=3")
	return &boundDelivery{b: b, r: r, w: w, pidfd: -1, missionToken: missionToken}, nil
}

// start starts the process detached with a pidfd; the host's copy of the read end is closed whether it started or not.
func (d *boundDelivery) start(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, PidFD: &d.pidfd}
	err := cmd.Start()
	d.r.Close()
	if err != nil {
		return err
	}
	if d.pidfd < 0 { // a kernel that gave no pidfd cannot run a bound turn
		_ = cmd.Process.Kill()
		return errors.New("the kernel gave no pidfd for the turn, so it cannot be bound")
	}
	return nil
}

// kill ends the turn's leader through its own pidfd (never by pid or group).
// setOnKill sets the hook called before the leader is killed.
func (d *boundDelivery) setOnKill(f func()) { d.onKill = f }

func (d *boundDelivery) kill() {
	if d.b != nil && d.b.beforeKill != nil {
		d.b.beforeKill()
	}
	if d.onKill != nil {
		d.onKill() // before the signal: the exit it causes must find the turn already marked as being stopped
	}
	if d.pidfd >= 0 {
		_ = unix.PidfdSendSignal(d.pidfd, unix.SIGKILL, nil, 0)
	}
	if d.b != nil && d.b.afterKill != nil {
		d.b.afterKill()
	}
}

// close releases the pipe and the pidfd; safe to call once.
func (d *boundDelivery) close() {
	d.w.Close()
	d.r.Close()
	if d.pidfd >= 0 {
		unix.Close(d.pidfd)
		d.pidfd = -1
	}
}

// boundToken is one token of a launch on its way to the frame.
type boundToken struct {
	kind   procbind.Kind
	id     string
	secret string
	scope  string
	gen    procbind.Generation
	mint   procbind.MintToken
	h      procbind.Handle
}

// deliver runs steps 2 to 5 for the turn whose process is pid with start stamp. A mission child's turn gets a second
// token, its task token, in the same transaction. On error nothing was delivered and the process has been killed; the
// caller waits for it as for any other turn.
func (d *boundDelivery) deliver(profile, conv, turn string, pid int, stamp string, revoke func() error) error {
	b := d.b
	fail := func(err error) error {
		d.kill()
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	generation, ok := b.host.Generation(profile)
	if !ok {
		return fail(errors.New("the binding service has no generation for this profile"))
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	// what this launch carries: the session token, and for a mission child its task token
	toks := []*boundToken{{kind: procbind.KindSession, scope: procbind.SessionScope(conv, turn)}}
	toks[0].mint = procbind.MintToken{Kind: procbind.KindSession, ConversationID: conv, TurnID: turn, Expires: now.Add(coderhost.SessionTokenTTL)}
	if c := d.child; c != nil {
		toks = append(toks, &boundToken{kind: procbind.KindMission, scope: procbind.MissionScope(c.task.SessionID, c.task.TaskID)})
		toks[1].mint = procbind.MintToken{Kind: procbind.KindMission, Owner: c.owner, Role: "worker", Actor: dynorg.MissionWorkerName(c.task.SessionID, c.task.TaskID),
			Session: c.task.SessionID, Task: c.task.TaskID, Expires: now.Add(childTurnLimit + childTokenMargin)}
	}
	for _, t := range toks {
		prefix := procbind.SessionPrefixV2
		if t.kind == procbind.KindMission {
			prefix = procbind.TaskPrefixV2
		}
		var hash string
		var err error
		if t.secret, hash, err = procbind.NewSecret(prefix); err != nil {
			return fail(err)
		}
		if t.id, err = procbind.NewTokenID(); err != nil {
			return fail(err)
		}
		t.mint.ID, t.mint.Hash, t.mint.Scope = t.id, hash, t.scope
	}
	zeroSecrets := func() {
		for _, t := range toks {
			t.secret = ""
		}
	}
	defer zeroSecrets()
	binder := b.binder
	// every entry has a handle of its own: a dup of the pidfd
	registered := []*boundToken{}
	undo := func(err error, minted bool) error {
		for _, t := range registered {
			binder.Reap(profile, t.id, t.gen)
		}
		d.kill()
		if minted {
			if rerr := revoke(); rerr != nil {
				err = errors.Join(err, &coderhost.TokensLiveError{Err: rerr})
			}
			if d.child != nil {
				if rerr := d.child.revokeOwner(); rerr != nil {
					err = errors.Join(err, &coderhost.TokensLiveError{Err: rerr})
				}
			}
		}
		return err
	}
	for _, t := range toks {
		dupfd, err := unix.FcntlInt(uintptr(d.pidfd), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return undo(fmt.Errorf("could not hold the turn's pidfd: %w", err), false)
		}
		h := procbind.HandleFromPidfd(dupfd)
		if !h.Alive() {
			_ = h.Close()
			return undo(errors.New("the turn ended before its credentials could be bound"), false)
		}
		spec := procbind.EntrySpec{TokenID: t.id, Kind: t.kind, Profile: profile, Scope: t.scope, Expires: t.mint.Expires}
		if t.kind == procbind.KindSession {
			spec.Session, spec.Turn = conv, turn
		} else {
			spec.Owner = t.mint.Owner
		}
		gen, err := binder.Register(spec, h) // the registry closes the handle it was given on an error
		if err != nil {
			return undo(fmt.Errorf("could not register the turn's token: %w", err), false)
		}
		t.gen = gen
		registered = append(registered, t)
	}
	key, err := procbind.RowKey(ctx, b.db, profile) // the mint: it may create the profile's key
	if err != nil {
		return undo(fmt.Errorf("could not get the row key: %w", err), false)
	}
	if b.beforeMint != nil {
		b.beforeMint()
	}
	mints := make([]procbind.MintToken, len(toks))
	for i, t := range toks {
		mints[i] = t.mint
	}
	req := procbind.MintRequest{
		Profile: profile, WorkerPID: pid, WorkerStamp: stamp, HostGeneration: generation, Now: now, Tokens: mints,
		Check: func(ctx context.Context, x procbind.DBTX) error {
			var current string
			if err := x.QueryRowContext(ctx, `SELECT turn_id FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profile, conv).Scan(&current); err != nil {
				return errors.New("the session is gone")
			}
			if current != turn {
				return errors.New("the session's current turn is not this turn")
			}
			if c := d.child; c != nil {
				ok, err := coderhost.ChildAttemptCurrentIn(ctx, x, profile, conv, c.task)
				if err != nil || !ok {
					return errors.New("the child ended or was replaced while its turn started")
				}
			}
			return nil
		},
	}
	if err := validateMint(req, key, profile); err != nil {
		return undo(err, false)
	}
	if err = procbind.MintTx(ctx, b.db, key, req); err != nil {
		// an error does not prove that nothing was written (a commit can fail after it was applied): every token of the
		// mint is ended by its own id, so a row that did land is revoked and cannot be accepted
		err = fmt.Errorf("could not bind the turn's token: %w", err)
		for _, t := range toks {
			if rerr := b.revokeByID(profile, t.kind, t.id); rerr != nil {
				err = errors.Join(err, &coderhost.TokensLiveError{Err: rerr})
			}
		}
		return undo(err, false)
	}
	if b.afterMint != nil {
		b.afterMint()
	}
	for _, t := range toks {
		if rv, err := procbind.IsRevoked(ctx, b.db, t.kind, t.id); err != nil || rv {
			return undo(errors.New("the turn's token was revoked before it was delivered"), true)
		}
	}
	for _, t := range toks {
		if err := binder.Activate(profile, t.id, t.gen); err != nil {
			return undo(errors.New("the turn's registration ended before it was delivered"), true)
		}
	}
	frame := procbind.CredFrame{SessionToken: procbind.Secret(toks[0].secret)}
	if len(toks) > 1 {
		frame.TaskToken = procbind.Secret(toks[1].secret)
	}
	if d.missionToken != "" {
		frame.MissionConfirmation = procbind.Secret(d.missionToken)
	}
	raw, err := frame.Encode()
	frame.Zero()
	if err != nil {
		return undo(err, true)
	}
	_, werr := d.w.Write(raw) // one atomic write: the whole frame is within PIPE_BUF
	procbind.Secret(raw).Zero()
	if cerr := d.w.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return undo(fmt.Errorf("could not hand the credentials to the turn: %w", werr), true)
	}
	return nil
}

// validateMint is the caller's check of what MintTx does not look at: the request names the profile whose key signs the
// rows, every token's scope belongs to its kind, and it ends after the mint.
func validateMint(req procbind.MintRequest, key []byte, profile string) error {
	if len(key) == 0 || req.Profile != profile {
		return errors.New("the mint does not name the profile whose key signs it")
	}
	for _, t := range req.Tokens {
		want := "session:"
		if t.Kind == procbind.KindMission {
			want = "mission:"
		}
		if !strings.HasPrefix(t.Scope, want) {
			return fmt.Errorf("the scope of a %s token is not a %s scope", t.Kind, t.Kind)
		}
		if !t.Expires.After(req.Now) {
			return errors.New("the token would already be expired")
		}
	}
	return nil
}

// revokeByID ends one v2 token by its id (and its live registration), whether or not its mint is known to have landed.
// It runs under a context of its own: the one that failed the mint may be the very deadline that has run out.
func (b *boundLaunch) revokeByID(profile string, kind procbind.Kind, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sel := procbind.Selector{Profile: profile, Kind: kind, TokenID: id}
	return procbind.Revoker{DB: b.db, Binder: b.binder}.Run(ctx, []procbind.Kind{kind}, procbind.Scope{Profile: profile, TokenID: id},
		func(*sql.Conn) ([]procbind.Selector, error) { return []procbind.Selector{sel}, nil })
}
