package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// What a conversation's turns leave outside the database is cleaned up in two steps, so that no
// filesystem work happens while the delete holds its write transaction:
//
//  1. Inside the delete's transaction the files are only LISTED, from the database (the turns, and
//     the attachment copies the host recorded in the turns' started events, each with the device and
//     inode of the copy as it was made), and the list is written as a row in configs
//     (file-cleanup/<profile>/<conversation>) in the same transaction. If the delete does not commit,
//     the row does not exist.
//  2. After the commit the row is run: the files go, and the row is removed. A crash between the two
//     leaves the row, and `chat history reconcile` (which the app runs at startup) runs it.
//
// What is removed is fenced by what was recorded, not by a name: a copy only if it still is the very
// file that was made (same device and inode), a turn's log only if it was last written before the
// delete (an id given to a new turn later keeps its log). Nothing is followed through a link and every
// folder is opened as a handle checked against what was just inspected. The row itself is not trusted:
// the folder the logs are in is worked out again from the host's own state folder when the row is run,
// log names must look like the host's, and a row with a time that cannot be read removes no log.

// cleanupPrefix starts the name of every cleanup row in configs.
const cleanupPrefix = "file-cleanup/"

// cleanupRetention is how long a row whose files could not all be dealt with is kept for another try.
const cleanupRetention = 7 * 24 * time.Hour

// logFileName is what the host's per-turn files are called.
var logFileName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}\.(log|prompt)$`)

// copyRef is one attachment copy the host made: its path relative to the attachments folder and the
// device and inode it had then.
type copyRef struct {
	Rel string `json:"rel"`
	Dev uint64 `json:"dev"`
	Ino uint64 `json:"ino"`
}

// cleanupIntent is what the delete listed.
type cleanupIntent struct {
	DeletedAt string    `json:"deleted_at"`
	Logs      []string  `json:"logs,omitempty"`
	Cwd       string    `json:"cwd,omitempty"`
	Copies    []copyRef `json:"copies,omitempty"`
}

func (in cleanupIntent) attachRoot() string {
	return filepath.Join(in.Cwd, ".monoagent", "attachments")
}

func cleanupName(profileID, conversationID string) string {
	return cleanupPrefix + profileID + "/" + conversationID
}

// recordCleanup lists the conversation's files from the database, on the delete's own connection, and
// stores the list in configs. It touches no file.
func recordCleanup(ctx context.Context, conn *sql.Conn, profileID, conversationID string) error {
	in := cleanupIntent{DeletedAt: time.Now().UTC().Format(time.RFC3339Nano)}
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
	for _, id := range ids {
		if hostID.MatchString(id) {
			in.Logs = append(in.Logs, id+".log", id+".prompt")
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
				if rel, err := filepath.Rel(root, filepath.Clean(a.Path)); err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
					in.Copies = append(in.Copies, copyRef{Rel: rel, Dev: a.Dev, Ino: a.Ino})
				}
			}
		}
	}
	if len(in.Logs) == 0 && len(in.Copies) == 0 {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `INSERT OR REPLACE INTO configs (name, config_data, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		cleanupName(profileID, conversationID), string(raw))
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

// afterLstat is a seam for tests: it runs between a folder being inspected and being opened.
var afterLstat = func(path string) {}

// skipFileCleanup is a seam for tests: a delete that commits and then "crashes" before the cleanup runs.
var skipFileCleanup = false

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

// run removes what the intent lists. logDir is the host's own turns folder (never taken from the row).
// What is gone already is fine; what could not be dealt with is returned, and the caller keeps the
// row for another try. The device and inode of a copy, and the time of a log, decide what is theirs
// to remove (see the top of this file). The conversation's folder is the conversation's own and is
// opened as it is; the folders below it are opened through it as pinned handles. A mount beneath an
// opened folder is not detected, which the identity fence makes harmless: only the recorded file goes.
func (in cleanupIntent) run(logDir string, warn func(format string, a ...any)) error {
	var failed []error
	fail := func(format string, a ...any) {
		err := fmt.Errorf(format, a...)
		warn("%v", err)
		failed = append(failed, err)
	}
	// A time that cannot be read removes no log: the fence cannot be applied.
	if deleted, err := time.Parse(time.RFC3339Nano, in.DeletedAt); err == nil && len(in.Logs) > 0 {
		root, err := openPinnedDir(logDir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			fail("could not open %s: %v", logDir, err)
		default:
			for _, name := range in.Logs {
				if !logFileName.MatchString(name) {
					continue
				}
				fi, err := root.Lstat(name)
				if err != nil {
					continue
				}
				if !fi.Mode().IsRegular() || fi.ModTime().After(deleted) {
					continue // not a file the turn wrote, or written after the delete: a new turn's
				}
				if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
					fail("could not remove %s: %v", filepath.Join(logDir, name), err)
				}
			}
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
			fail("could not open %s: %v", in.Cwd, err)
		}
		return errors.Join(failed...)
	}
	defer cwd.Close()
	meta, err := openPinnedSub(cwd, ".monoagent", filepath.Join(in.Cwd, ".monoagent"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fail("%v", err)
		}
		return errors.Join(failed...)
	}
	defer meta.Close()
	root, err := openPinnedSub(meta, "attachments", attach)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fail("%v", err)
		}
		return errors.Join(failed...)
	}
	defer root.Close()
	dirs := map[string]bool{}
	for _, c := range in.Copies {
		fi, err := root.Lstat(c.Rel)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if dev, ino, ok := fileIdentity(fi); !ok || dev != c.Dev || ino != c.Ino {
			continue // not the file that was made
		}
		if err := root.Remove(c.Rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			fail("could not remove %s: %v", filepath.Join(attach, c.Rel), err)
			continue
		}
		if d := filepath.Dir(c.Rel); d != "." {
			dirs[d] = true
		}
	}
	for d := range dirs {
		_ = root.Remove(d) // only while empty
	}
	return errors.Join(failed...)
}

// runCleanups runs the cleanup rows named name (exactly), or, with prefix set, every row whose name
// starts with name, and removes each row once it is dealt with. A row that could not be dealt with is
// kept for another try for cleanupRetention; one whose JSON cannot be read is dropped. Safe to run
// twice: a file that is gone is fine.
func runCleanups(ctx context.Context, db *sql.DB, logDir, name string, prefix bool, warn func(format string, a ...any)) error {
	var rows *sql.Rows
	var err error
	if prefix {
		rows, err = db.QueryContext(ctx, `SELECT name, config_data FROM configs WHERE substr(name, 1, length(?)) = ?`, name, name)
	} else {
		rows, err = db.QueryContext(ctx, `SELECT name, config_data FROM configs WHERE name = ?`, name)
	}
	if err != nil {
		return err
	}
	type row struct{ name, data string }
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.name, &r.data); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range todo {
		var in cleanupIntent
		if json.Unmarshal([]byte(r.data), &in) == nil {
			if err := in.run(logDir, warn); err != nil {
				if at, perr := time.Parse(time.RFC3339Nano, in.DeletedAt); perr == nil && time.Since(at) < cleanupRetention {
					continue // kept: the next reconcile tries again
				}
			}
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM configs WHERE name = ?`, r.name); err != nil {
			return err
		}
	}
	return nil
}

func warnOnStderr(format string, a ...any) { fmt.Fprintf(os.Stderr, "warning: "+format+"\n", a...) }
