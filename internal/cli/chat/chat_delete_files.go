package chat

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/secrets"
)

// What a conversation's turns leave outside the database is cleaned up in two steps, so that no
// filesystem work happens while the delete holds its write transaction:
//
//  1. Inside the delete's transaction the files are only LISTED and the list is written as a row in
//     configs (file-cleanup/<profile>/<conversation>) in the same transaction. The list comes from the
//     database (the turns, and the attachment copies the host recorded in the turns' started events,
//     each with the device and inode of the copy as it was made) and from the identity of each turn's
//     log file, read just BEFORE the transaction (outside the lock) and kept only for turns that are
//     still the conversation's inside it. If the delete does not commit, the row does not exist.
//  2. After the commit the row is run: the files go, and the row is removed. A crash between the two
//     leaves the row, and `chat history reconcile` (which the app runs at startup) runs it.
//
// The row is authenticated: it carries a MAC under a key derived from the profile's vault data key (the
// primitive the coder host's mission confirmations use), and a row is run only if the MAC verifies and
// the conversation is really gone. It is also a reserved config family (file-cleanup/), which nothing
// but the delete writes. What is removed is fenced by what was recorded, not by a name: a file only if
// it still is the very file that was recorded (device and inode), and it is removed by moving it to a
// name nobody else knows inside a pinned folder, checking what was moved, and only then unlinking it.

// cleanupPrefix starts the name of every cleanup row in configs.
const cleanupPrefix = "file-cleanup/"

// cleanupKeyPurpose is the purpose of the key that signs the rows.
const cleanupKeyPurpose = "file-cleanup/v1"

// cleanupRetention is how long a row whose files could not all be dealt with is kept for another try.
const cleanupRetention = 7 * 24 * time.Hour

// logFileName is what the host's per-turn files are called.
var logFileName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}\.(log|prompt)$`)

// fileRef is one file that was recorded: a name relative to the folder it is in, and the device and
// inode it had then.
type fileRef struct {
	Rel string `json:"rel"`
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

// cleanupIntent is what the delete listed.
type cleanupIntent struct {
	Nonce     string    `json:"nonce"`
	DeletedAt string    `json:"deleted_at"`
	Logs      []fileRef `json:"logs,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	Copies    []fileRef `json:"copies,omitempty"`
}

// envelope is a stored row: the intent as JSON and its MAC.
type envelope struct {
	Intent json.RawMessage `json:"intent"`
	Mac    string          `json:"mac"`
}

func (in cleanupIntent) attachRoot() string {
	return filepath.Join(in.Cwd, ".monoagent", "attachments")
}

// cleanupName is the name of one delete's row: every delete has a row of its own (the nonce), so a row
// never overwrites another's.
func cleanupName(profileID, conversationID, nonce string) string {
	return cleanupConvPrefix(profileID, conversationID) + nonce
}

// cleanupConvPrefix starts the names of all the rows of one conversation.
func cleanupConvPrefix(profileID, conversationID string) string {
	return cleanupPrefix + profileID + "/" + conversationID + "/"
}

// splitCleanupName is the profile and the conversation a row name carries.
func splitCleanupName(name string) (profileID, conversationID string, ok bool) {
	rest, found := strings.CutPrefix(name, cleanupPrefix)
	if !found {
		return "", "", false
	}
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func cleanupMac(key []byte, name string, intent []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(name))
	m.Write([]byte{0})
	m.Write(intent)
	return hex.EncodeToString(m.Sum(nil))
}

// statLogs reads, outside any transaction, the identity of the host's log and prompt files of the
// conversation's turns. It opens the host's turns folder as a pinned root.
func statLogs(cfg *Deps, store *ai.AIStore, profileID, conversationID string) []fileRef {
	var names []string
	for cursor := ""; ; {
		turns, next, err := store.ListTurns(conversationID, profileID, cursor, 200)
		if err != nil {
			return nil
		}
		for _, t := range turns {
			if hostID.MatchString(t.ID) {
				names = append(names, t.ID+".log", t.ID+".prompt")
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(names) == 0 {
		return nil
	}
	root, err := openPinnedDir(filepath.Join(hostDir(cfg), "turns"))
	if err != nil {
		return nil
	}
	defer root.Close()
	var out []fileRef
	for _, n := range names {
		fi, err := root.Lstat(n)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if dev, ino, ok := fileIdentity(fi); ok {
			out = append(out, fileRef{Rel: n, Dev: dev, Ino: ino})
		}
	}
	return out
}

// recordCleanup lists the conversation's files from the database, on the delete's own connection, and
// stores the signed list in configs. logs are the log files whose identity was read before the
// transaction; only those of turns that are still the conversation's are kept. It touches no file. With
// no key (the vault's key could not be had) nothing is recorded and the files stay.
func recordCleanup(ctx context.Context, conn *sql.Conn, profileID, conversationID string, key []byte, logs []fileRef) error {
	if len(key) == 0 {
		warnOnStderr("the conversation's files are left in place: the key that signs their cleanup is not available")
		return nil
	}
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	in := cleanupIntent{Nonce: hex.EncodeToString(nonce), DeletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	var cwd sql.NullString
	err := conn.QueryRowContext(ctx, `SELECT cwd FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`, conversationID, profileID).Scan(&cwd)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // nothing to list for a conversation that is not there
	}
	if err != nil {
		return fmt.Errorf("reading the conversation's folder: %w", err)
	}
	if cwd.String != "" && filepath.IsAbs(cwd.String) {
		in.Cwd = filepath.Clean(cwd.String)
	}
	ids, err := queryStrings(ctx, conn, `SELECT id FROM ai_chat_turns WHERE conversation_id = ? AND profile_id = ?`, conversationID, profileID)
	if err != nil {
		return fmt.Errorf("listing the conversation's turns: %w", err)
	}
	mine := map[string]bool{}
	for _, id := range ids {
		mine[id] = true
	}
	for _, l := range logs {
		if logFileName.MatchString(l.Rel) && mine[strings.TrimSuffix(strings.TrimSuffix(l.Rel, ".log"), ".prompt")] {
			in.Logs = append(in.Logs, l)
		}
	}
	if in.Cwd != "" {
		root := in.attachRoot()
		payloads, err := queryStrings(ctx, conn, `SELECT payload FROM ai_chat_events WHERE profile_id = ? AND conversation_id = ? AND type = ?`,
			profileID, conversationID, string(chatevents.EventTurnStarted))
		if err != nil {
			return fmt.Errorf("listing the conversation's attachments: %w", err)
		}
		for _, raw := range payloads {
			var p chatevents.TurnStartedPayload
			if json.Unmarshal([]byte(raw), &p) != nil {
				continue
			}
			for _, a := range p.Attachments {
				if a.Dev == 0 && a.Ino == 0 {
					continue // made before the copy's identity was recorded: not removed
				}
				if rel, err := filepath.Rel(root, filepath.Clean(a.Path)); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					in.Copies = append(in.Copies, fileRef{Rel: rel, Dev: a.Dev, Ino: a.Ino})
				}
			}
		}
	}
	if len(in.Logs) == 0 && len(in.Copies) == 0 {
		return nil
	}
	name := cleanupName(profileID, conversationID, in.Nonce)
	intent, err := json.Marshal(in)
	if err != nil {
		return err
	}
	data, err := json.Marshal(envelope{Intent: intent, Mac: cleanupMac(key, name, intent)})
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO configs (name, config_data, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, name, string(data))
	if err != nil {
		return fmt.Errorf("recording the files to clean up: %w", err)
	}
	return nil
}

func queryStrings(ctx context.Context, conn *sql.Conn, q string, args ...any) ([]string, error) {
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// statIdentity reads a file's device and inode; a seam for tests.
var statIdentity = fileIdentity

// Seams for tests.
var (
	// afterLstat runs between a folder being inspected and being opened.
	afterLstat = func(path string) {}
	// leafHook runs around the removal of one file: stage "before" (just before it is moved aside) and
	// "moved" (just after), with the folder it is in and its name.
	leafHook = func(stage string, root *os.Root, rel string) {}
	// skipFileCleanup makes a delete commit and then "crash" before the cleanup runs.
	skipFileCleanup = false
)

// openPinnedDir opens dir as a root, and only if it is the folder that was inspected: it must be a real
// folder (not a link) when looked at, and the folder opened must be that same one. A folder swapped
// for a link between the two is refused, and once it is open the handle names the folder whatever
// happens to the path after.
func openPinnedDir(dir string) (*os.Root, error) {
	lst, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !lst.IsDir() {
		return nil, fmt.Errorf("%s is not a plain folder", dir)
	}
	afterLstat(dir)
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	st, err := r.Stat(".")
	if err != nil || !os.SameFile(lst, st) {
		r.Close()
		return nil, fmt.Errorf("%s changed while it was being opened", dir)
	}
	return r, nil
}

// openPinnedSub is openPinnedDir for the folder name inside parent.
func openPinnedSub(parent *os.Root, name, full string) (*os.Root, error) {
	lst, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !lst.IsDir() {
		return nil, fmt.Errorf("%s is not a plain folder", full)
	}
	afterLstat(full)
	r, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	st, err := r.Stat(".")
	if err != nil || !os.SameFile(lst, st) {
		r.Close()
		return nil, fmt.Errorf("%s changed while it was being opened", full)
	}
	return r, nil
}

// tombstonePrefix starts the name a file is moved to before it is checked and unlinked.
const tombstonePrefix = ".removing-"

// removeLeftovers removes the files a crash left between the move and the unlink: entries named like a
// tombstone, in the folders of the recorded files, that still are a recorded file (same device and
// inode). Anything else with such a name is left alone.
func removeLeftovers(root *os.Root, refs []fileRef, warn func(format string, a ...any)) {
	type id struct{ dev, ino uint64 }
	want := map[id]bool{}
	dirs := map[string]bool{}
	for _, r := range refs {
		want[id{r.Dev, r.Ino}] = true
		dirs[filepath.Dir(r.Rel)] = true
	}
	for d := range dirs {
		f, err := root.Open(d)
		if err != nil {
			continue
		}
		entries, _ := f.ReadDir(-1)
		f.Close()
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), tombstonePrefix) {
				continue
			}
			rel := filepath.Join(d, e.Name())
			fi, err := root.Lstat(rel)
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			if dev, ino, ok := statIdentity(fi); ok && want[id{dev, ino}] {
				if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
					warn("could not remove %s: %v", rel, err)
				}
			}
		}
	}
}

// removeRecorded removes the file ref names inside root, if it still is the recorded file. The name is
// not trusted between looking and unlinking, so the entry is first MOVED to a name nobody else knows,
// and what was moved is checked there: only a regular file with the recorded device and inode is
// unlinked. Anything else that was moved (the name had been taken by another file since) is put back
// under its name if that is free, and otherwise left under the unique name and reported. Whatever
// happens, a file that was not recorded is never unlinked. Remaining: a process that can write this
// folder and also finds the unique name within the instant between the move and the unlink.
func removeRecorded(root *os.Root, ref fileRef, warn func(format string, a ...any)) error {
	leafHook("before", root, ref.Rel)
	rb := make([]byte, 8)
	if _, err := rand.Read(rb); err != nil {
		return err
	}
	// A short name of its own in the same folder, whatever the file is called (a long name plus a suffix
	// can pass the limit and fail for good).
	tomb := filepath.Join(filepath.Dir(ref.Rel), tombstonePrefix+hex.EncodeToString(rb))
	if err := root.Rename(ref.Rel, tomb); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("could not set %s aside: %w", ref.Rel, err)
	}
	leafHook("moved", root, ref.Rel)
	fi, err := root.Lstat(tomb)
	if err != nil {
		return fmt.Errorf("could not look at what was moved from %s: %w", ref.Rel, err)
	}
	if dev, ino, ok := statIdentity(fi); fi.Mode().IsRegular() && ok && dev == ref.Dev && ino == ref.Ino {
		if err := root.Remove(tomb); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("could not remove %s: %w", ref.Rel, err)
		}
		return nil
	}
	// Not the recorded file: put it back if its name is free.
	if _, err := root.Lstat(ref.Rel); errors.Is(err, fs.ErrNotExist) {
		if err := root.Rename(tomb, ref.Rel); err == nil {
			return nil
		}
	}
	warn("%s is not the file that was recorded; it was left as %s", ref.Rel, tomb)
	return nil
}

// run removes what the intent lists. logDir is the host's own turns folder (never taken from the row).
// What is gone already is fine; what could not be dealt with is returned, and the caller keeps the row
// for another try. The conversation's folder is the conversation's own and is opened as it is; the
// folders below it are opened through it as pinned handles. A mount beneath an opened folder is not
// detected, which the identity fence makes harmless: only the recorded file goes.
func (in cleanupIntent) run(logDir string, warn func(format string, a ...any)) error {
	var failed []error
	fail := func(err error) {
		warn("%v", err)
		failed = append(failed, err)
	}
	if len(in.Logs) > 0 {
		root, err := openPinnedDir(logDir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			fail(fmt.Errorf("could not open %s: %w", logDir, err))
		default:
			for _, l := range in.Logs {
				if !logFileName.MatchString(l.Rel) {
					continue
				}
				if err := removeRecorded(root, l, warn); err != nil {
					fail(err)
				}
			}
			removeLeftovers(root, in.Logs, warn)
			root.Close()
		}
	}
	if in.Cwd == "" || !filepath.IsAbs(in.Cwd) || len(in.Copies) == 0 {
		return errors.Join(failed...)
	}
	attach := in.attachRoot()
	cwd, err := os.OpenRoot(in.Cwd)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fail(fmt.Errorf("could not open %s: %w", in.Cwd, err))
		}
		return errors.Join(failed...)
	}
	defer cwd.Close()
	meta, err := openPinnedSub(cwd, ".monoagent", filepath.Join(in.Cwd, ".monoagent"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fail(err)
		}
		return errors.Join(failed...)
	}
	defer meta.Close()
	root, err := openPinnedSub(meta, "attachments", attach)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fail(err)
		}
		return errors.Join(failed...)
	}
	defer root.Close()
	dirs := map[string]bool{}
	for _, c := range in.Copies {
		if err := removeRecorded(root, c, warn); err != nil {
			fail(err)
			continue
		}
		if d := filepath.Dir(c.Rel); d != "." {
			dirs[d] = true
		}
	}
	removeLeftovers(root, in.Copies, warn)
	for d := range dirs {
		_ = root.Remove(d) // only while empty
	}
	return errors.Join(failed...)
}

// runCleanups runs the cleanup rows named name (exactly), or, with prefix set, every row whose name
// starts with name. A row is run only if its MAC verifies under the profile's key and its conversation
// is really gone; otherwise it is dropped with a warning. A row that was dealt with is removed, by its
// name AND the exact data that was read, so a row a newer delete wrote in between stays. A row whose
// files could not all be dealt with is kept for another try for cleanupRetention. Safe to run twice.
func runCleanups(ctx context.Context, db *sql.DB, logDir, name string, prefix bool, warn func(format string, a ...any)) error {
	const age = `COALESCE(CAST(strftime('%s', 'now') AS INTEGER) - CAST(strftime('%s', created_at) AS INTEGER), 0)`
	var rows *sql.Rows
	var err error
	if prefix {
		rows, err = db.QueryContext(ctx, `SELECT name, config_data, `+age+` FROM configs WHERE substr(name, 1, length(?)) = ?`, name, name)
	} else {
		rows, err = db.QueryContext(ctx, `SELECT name, config_data, `+age+` FROM configs WHERE name = ?`, name)
	}
	if err != nil {
		return err
	}
	type row struct {
		name, data string
		ageSeconds int64
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.data, &r.ageSeconds); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	keys := map[string][]byte{}
	for _, r := range todo {
		drop := true
		if in, err := verifiedIntent(ctx, db, keys, r.name, r.data, warn); err != nil {
			var retry errRetry
			if errors.As(err, &retry) && time.Duration(r.ageSeconds)*time.Second < cleanupRetention {
				// The machine could not say (the vault's key, the database): the row is kept, tried again
				// later, and only given up on when it is old.
				warn("a cleanup row is kept for another try: %v", err)
				drop = false
			} else {
				warn("a cleanup row was dropped: %v", err)
			}
		} else if runErr := in.run(logDir, warn); runErr != nil {
			if at, perr := time.Parse(time.RFC3339Nano, in.DeletedAt); perr == nil && time.Since(at) < cleanupRetention {
				drop = false // kept: the next reconcile tries again
			}
		}
		if !drop {
			continue
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM configs WHERE name = ? AND config_data = ?`, r.name, r.data); err != nil {
			return err
		}
	}
	return nil
}

// deriveKey is the vault key derivation; a seam for tests.
var deriveKey = secrets.DeriveKey

// keyTimeout bounds how long getting the vault's key may take (a locked keychain can wait for a prompt).
var keyTimeout = 10 * time.Second

// deriveKeyWithin is secrets.DeriveKey that gives up after keyTimeout: the answer then is an error, and the
// caller leaves things as they are.
func deriveKeyWithin(ctx context.Context, db *sql.DB, profileID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, keyTimeout)
	defer cancel()
	type result struct {
		key []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		k, err := deriveKey(ctx, db, profileID, cleanupKeyPurpose)
		done <- result{k, err}
	}()
	select {
	case r := <-done:
		return r.key, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("the vault's key was not had within %s: %w", keyTimeout, ctx.Err())
	}
}

// errRetry marks a failure of the machine, not of the row: the row is kept and tried again later.
type errRetry struct{ error }

func (e errRetry) Unwrap() error { return e.error }

// verifiedIntent is the row's intent if its MAC verifies and its conversation is gone.
func verifiedIntent(ctx context.Context, db *sql.DB, keys map[string][]byte, name, data string, warn func(string, ...any)) (cleanupIntent, error) {
	var in cleanupIntent
	profileID, conversationID, ok := splitCleanupName(name)
	if !ok {
		return in, errors.New("its name is not a cleanup row's")
	}
	key, have := keys[profileID]
	if !have {
		k, err := deriveKeyWithin(ctx, db, profileID)
		if err != nil {
			return in, errRetry{fmt.Errorf("its key could not be had: %w", err)}
		}
		key = k
		keys[profileID] = k
	}
	var env envelope
	if json.Unmarshal([]byte(data), &env) != nil || !hmac.Equal([]byte(cleanupMac(key, name, env.Intent)), []byte(env.Mac)) {
		return in, errors.New("its signature does not verify")
	}
	if json.Unmarshal(env.Intent, &in) != nil {
		return in, errors.New("it cannot be read")
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`, conversationID, profileID).Scan(&n); err != nil {
		return in, errRetry{err}
	}
	if n > 0 {
		return in, errors.New("its conversation still exists")
	}
	return in, nil
}

func warnOnStderr(format string, a ...any) { fmt.Fprintf(os.Stderr, "warning: "+format+"\n", a...) }
