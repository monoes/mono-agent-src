package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/daemonhb"
)

// runGateVersion is 1 for the host whose open-ended dispatch gate resolves binding as the start does (unset is on where the machine
// can run it; a run that started unbound keeps going). Raise it when the start and the gate must agree on something new.
const runGateVersion = 1

// What the person does after stopping a stale host, per place the refusal is shown.
const (
	StaleThenSend  = "send again"
	StaleThenStart = "run `doomsday start` again"
	StaleThenApp   = "start again from the app"
)

// hostStopCommand is the exact command that stops the host of the database at dbPath: --db-path is named when it is not the
// default database, so the operator cannot stop a different host.
func hostStopCommand(dbPath string) string {
	const def = "~/.monoagent/monoagent.db"
	if dbPath == "" || clicore.ExpandPath(dbPath) == clicore.ExpandPath(def) {
		return "monoagentcli coder host stop"
	}
	return fmt.Sprintf("monoagentcli --db-path %q coder host stop", clicore.ExpandPath(dbPath))
}

// StaleHostReason is "" unless a coder host is running whose info file shows it predates runGateVersion. Such a host refuses
// every turn of a run the current start made ("no run without binding") and every placed turn, so the start, a send and the app's
// availability refuse up front with this text instead of leaving a run or a command that cannot dispatch. then says what to do after
// the stop (StaleThen*). It only reads: nothing is created and no lock is taken (the availability probe is polled). A host.json
// whose process is gone is the file of a killed host that a starting host has not yet rewritten, so it is not called stale; one
// that cannot be read is a starting host too.
func StaleHostReason(dbPath, then string) string {
	if !HostRunningAt(dbPath) {
		return ""
	}
	cfg := &Deps{DBPath: func() string { return dbPath }}
	info, ok := readHostInfo(cfg)
	if !ok || info.RunGate >= runGateVersion {
		return ""
	}
	if info.Stamp != "" && stampOf(info.PID) != info.Stamp {
		return ""
	}
	return "the running coder host is older than this program and would refuse every turn of an open-ended run (\"no run without binding\") and every placed turn: " +
		"stop it with `" + hostStopCommand(dbPath) + "` (running coder turns keep running), then " + then + ": a current host starts by itself"
}

// ensureCurrentHost is what every path that queues a send for the host asks before it queues: nil when no host runs (one is
// started after the command is queued) or the running host is current. A stale host (StaleHostReason) is stopped, and the caller's
// ensureHost then starts a current one, only when nothing depends on it (hostBusy); otherwise the command is not queued and the
// person gets the actionable text, never a stale host handed a command it would fail ("placing a turn needs a bound launch").
func ensureCurrentHost(cfg *Deps) error { return ensureCurrentHostExcept(cfg, 0) }

// ensureCurrentHostExcept is ensureCurrentHost for an answer: the parked question with id exceptQuestion (coder_session_questions)
// is the one being answered, and it and its person item do not make the host busy. 0 excludes nothing.
func ensureCurrentHostExcept(cfg *Deps, exceptQuestion int64) error {
	return ensureCurrentHostThen(cfg, exceptQuestion, StaleThenSend)
}

// EnsureCurrentHostForStart is ensureCurrentHost for `doomsday start`: the same safety rule and the same stop (hostBusy, the
// restart lock, the identity checks), with the refusal worded for the start. The start calls it only after its read-only
// binding check found a stale host; the app's availability never does (it only reports).
func EnsureCurrentHostForStart(cfg *Deps) error { return ensureCurrentHostThen(cfg, 0, StaleThenStart) }

func ensureCurrentHostThen(cfg *Deps, exceptQuestion int64, then string) error {
	why := StaleHostReason(cfg.DBPath(), then)
	if why == "" {
		return nil
	}
	busy := hostBusy(cfg, exceptQuestion)
	if busy == "" {
		err := stopStaleHost(cfg, exceptQuestion)
		if err == nil {
			return nil
		}
		busy = "it could not be stopped by itself (" + err.Error() + ")"
	}
	return clicore.InvalidInput("%s; it was not restarted automatically because %s", why, busy)
}

// hostBusy says what depends on the running host, "" when nothing does: an open-ended run, a coder turn that has not ended, a
// queued command, an open ask, an open parked question other than exceptQuestion, or an open item waiting for the person (a plan, a
// budget request, an ancestor question; not a stopped session's, and a parked question's own item only when it is exceptQuestion's). Anything it cannot read counts as busy. It reads the database only; the
// host's own lock is held by the host, so the check cannot be atomic with the stop: a turn that starts in between is left running by
// the stop (a stopped host leaves its turns running and the next host re-attaches to them), so the race costs a restart, never a turn.
func hostBusy(cfg *Deps, exceptQuestion int64) string {
	db, err := cfg.OpenDB()
	if err != nil {
		return "the database could not be read"
	}
	defer db.Close()
	if n, err := coderhost.OpenRunCount(context.Background(), db.DB); err != nil {
		return "the state of the host's work could not be read"
	} else if n > 0 {
		return "an open-ended run is open"
	}
	checks := []struct {
		what, q string
		args    []any
	}{
		{"a coder turn is running", `SELECT COUNT(*) FROM ai_chat_turns WHERE status NOT IN ('completed','failed','cancelled','interrupted')`, nil},
		{"a command is queued for the host", `SELECT COUNT(*) FROM coder_session_commands WHERE status = 'pending'`, nil},
		{"a question is waiting for an answer", `SELECT COUNT(*) FROM coder_session_asks WHERE status = 'open'`, nil},
		{"another parked question is waiting for an answer", `SELECT COUNT(*) FROM coder_session_questions WHERE status = 'open' AND id <> ?`, []any{exceptQuestion}},
	}
	for _, c := range checks {
		var n int
		if err := db.DB.QueryRow(c.q, c.args...).Scan(&n); err != nil {
			return "the state of the host's work could not be read"
		}
		if n > 0 {
			return c.what
		}
	}
	return openPersonItemsBusy(db.DB, exceptQuestion)
}

// openPersonItemsBusy says whether something is waiting for the person. A person item has no status of its own: it is open when
// the row it points at is open and its session is not stopped (Store.OpenPersonItems, per profile), so an item left behind by a
// stopped session or an answered question does not hold a restart. An unreadable item list counts as open.
func openPersonItemsBusy(db *sql.DB, exceptQuestion int64) string {
	const unreadable = "the state of the host's work could not be read"
	rows, err := db.Query(`SELECT DISTINCT profile_id FROM coder_session_person_items`)
	if err != nil {
		return unreadable
	}
	var profiles []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return unreadable
		}
		profiles = append(profiles, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return unreadable
	}
	store := coderhost.NewStore(db)
	for _, p := range profiles {
		items, err := store.OpenPersonItems(p)
		if err != nil {
			return unreadable
		}
		for _, it := range items {
			if it.Kind == coderhost.ItemParkedQuestion && it.RefID == exceptQuestion {
				continue
			}
			return "something is waiting for the person"
		}
	}
	return ""
}

// restartLockWait is how long a restart waits for another CLI's restart to finish.
var restartLockWait = 10 * time.Second

// stopStaleHost stops the running stale host the way `coder host stop` does. Restarts are serialized by a lock file, and under
// it everything is read again: a CLI that lost the race finds the host already current (or busy) and signals nothing, so it can
// never end the current host another CLI just started. The pid must be a process running `coder host run`, not just whatever
// host.json names. The check of busyness stays a read of the database (see hostBusy).
func stopStaleHost(cfg *Deps, exceptQuestion int64) error {
	if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
		return err
	}
	var release func()
	for deadline := time.Now().Add(restartLockWait); ; {
		r, err := daemonhb.LockFile(filepath.Join(hostDir(cfg), "host-restart.lock"))
		if err == nil {
			release = r
			break
		}
		if !errors.Is(err, daemonhb.ErrHeld) {
			return fmt.Errorf("the restart lock %s could not be taken: %w", filepath.Join(hostDir(cfg), "host-restart.lock"), err)
		}
		if time.Now().After(deadline) {
			return errors.New("another restart of the host is in progress (its lock was still held after the wait)")
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer release()
	if StaleHostReason(cfg.DBPath(), StaleThenSend) == "" { // only emptiness matters here, not the text
		return nil // another CLI restarted it (or it ended) while this one waited
	}
	if busy := hostBusy(cfg, exceptQuestion); busy != "" {
		return fmt.Errorf("it became busy: %s", busy)
	}
	info, ok := readHostInfo(cfg)
	infoPath := filepath.Join(hostDir(cfg), hostInfoFile)
	switch {
	case !ok:
		return fmt.Errorf("%s cannot be read, so the running host cannot be identified: stop it by hand with `%s`, or remove that file if no host is running, then try again", infoPath, hostStopCommand(cfg.DBPath()))
	case info.PID <= 1 || info.Stamp == "":
		return fmt.Errorf("%s names no process I can identify (it was written by a host too old to record one): stop the host by hand with `%s`", infoPath, hostStopCommand(cfg.DBPath()))
	case stampOf(info.PID) != info.Stamp:
		return fmt.Errorf("%s is left over from a host that is gone, and the host that runs now has not rewritten it yet: try again in a moment, or stop the host by hand with `%s`", infoPath, hostStopCommand(cfg.DBPath()))
	}
	if _, err := stopStaleProcess(cfg, info); err != nil {
		return fmt.Errorf("%w: stop the host by hand with `%s` if it really is the stale host", err, hostStopCommand(cfg.DBPath()))
	}
	for deadline := time.Now().Add(hostStopSettle); hostRunning(cfg) && time.Now().Before(deadline); {
		time.Sleep(50 * time.Millisecond)
	}
	if hostRunning(cfg) {
		return errors.New("it is still running after it was told to stop")
	}
	return nil
}
