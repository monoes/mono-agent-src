//go:build linux

package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// A refused turn that survives past the bound: the host settles the session and starts the next turn, whose token must
// survive the old process's late exit (the exit revokes only the refused turn's own tokens).
func TestALateExitOfARefusedTurnLeavesTheNextTurnsTokenAlone(t *testing.T) {
	oldGrace := hostTurnGrace
	hostTurnGrace = time.Hour // the stop does not kill the group within the test
	t.Cleanup(func() { hostTurnGrace = oldGrace })
	script := `trap '' TERM
d=$(dirname "$0")
echo $$ > "$d/pid"
p0=$PPID; while [ -d "$d" ] && [ ! -e "$d/release" ] && kill -0 $p0 2>/dev/null; do sleep 0.05; done
`
	r := newBoundRig(t, script)
	l := &execLauncher{bin: r.l.bin, dbPath: "/db/x.db", instance: "host-test", logDir: r.l.logDir, revokeBackoff: time.Millisecond,
		tokens: r.store.IssueSessionToken, revoke: r.store.RevokeSessionTokensErr, revokeTurn: r.store.RevokeTurnSessionTokensErr}
	turn, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	count := func(turnID string) int {
		var n int
		if err := r.db.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens WHERE conversation_id = 'c1' AND turn_id = ?`, turnID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("t1") != 1 {
		t.Fatalf("the first turn has %d tokens, want 1", count("t1"))
	}
	rf, ok := turn.(interface{ Refuse() error })
	if !ok {
		t.Fatalf("the launcher's turn %T cannot be refused", turn)
	}
	if err := rf.Refuse(); err != nil {
		t.Fatal(err)
	}
	if count("t1") != 0 {
		t.Error("the refused turn's own token was not ended at once")
	}
	// the host settled the session and started the next turn, while the old process (ignoring SIGTERM) still runs
	r.session("default", "c1", "t2")
	if _, err := r.store.IssueSessionToken("default", "c1", "t2"); err != nil {
		t.Fatal(err)
	}
	if count("t2") != 1 {
		t.Fatalf("the next turn has %d tokens, want 1", count("t2"))
	}
	if err := os.WriteFile(r.release, []byte("x"), 0o600); err != nil { // the old process finally exits
		t.Fatal(err)
	}
	_ = turn.Wait()
	if count("t2") != 1 {
		t.Error("the refused turn's late exit revoked the next turn's token")
	}
}

// The exit of a turn ends that turn's own tokens whenever it runs, even when it runs long after the process exited and the
// next turn has minted its token: the choice is made at launch, not when the gate gets round to refusing.
func TestADelayedExitCleanupNeverTouchesALaterTurnsToken(t *testing.T) {
	script := `d=$(dirname "$0")
echo $$ > "$d/pid"
p0=$PPID; while [ -d "$d" ] && [ ! -e "$d/release" ] && kill -0 $p0 2>/dev/null; do sleep 0.05; done
`
	r := newBoundRig(t, script)
	wide := 0
	unblock := make(chan struct{})
	l := &execLauncher{bin: r.l.bin, dbPath: "/db/x.db", instance: "host-test", logDir: r.l.logDir, revokeBackoff: time.Millisecond,
		tokens: r.store.IssueSessionToken,
		revoke: func(p, c string) error { wide++; return r.store.RevokeSessionTokensErr(p, c) },
		revokeTurn: func(p, c, turn string) error {
			<-unblock // the cleanup is delayed
			return r.store.RevokeTurnSessionTokensErr(p, c, turn)
		}}
	turn, err := l.Start("default", "c1", "t1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.release, []byte("x"), 0o600); err != nil { // the process exits BEFORE anyone refuses it
		t.Fatal(err)
	}
	if !r.gone(func() int {
		b, _ := os.ReadFile(filepath.Join(r.dir, "pid"))
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return n
	}()) {
		t.Fatal("the process did not exit")
	}
	r.session("default", "c1", "t2")
	if _, err := r.store.IssueSessionToken("default", "c1", "t2"); err != nil {
		t.Fatal(err)
	}
	close(unblock) // the delayed cleanup of the first turn runs now
	_ = turn.Wait()
	var n int
	if err := r.db.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens WHERE conversation_id = 'c1' AND turn_id = 't2'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the next turn's token was revoked by the first turn's delayed cleanup (%d rows, %v)", n, err)
	}
	if wide != 0 {
		t.Errorf("the conversation-wide revoke ran %d times for a turn whose id is known", wide)
	}
}

// A refused mission child's task token is dead at once, while its process is still running.
func TestARefusedChildsTaskTokenIsDeadWhileItsProcessStillRuns(t *testing.T) {
	f := newChildFix(t)
	dir := t.TempDir()
	bin := fakeChildBin(t, dir) // registers the reaper
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(strings.Replace(string(b), "#!/bin/sh\n", "#!/bin/sh\ntrap '' TERM\n", 1)), 0o755); err != nil { // the process outlives the stop
		t.Fatal(err)
	}
	oldGrace := hostTurnGrace
	hostTurnGrace = time.Hour
	t.Cleanup(func() { hostTurnGrace = oldGrace })
	store := coderhost.NewStore(f.db.DB)
	l := &execLauncher{bin: bin, dbPath: f.m.dbPath, instance: "host-test", logDir: filepath.Join(dir, "turns"), child: f.mint,
		revoke: store.RevokeSessionTokensErr, revokeTurn: store.RevokeTurnSessionTokensErr, revokeBackoff: time.Millisecond}
	turn, err := l.Start("default", f.child, "master-1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	owner := dynorg.TurnOwner("default", f.child, "master-1")
	live := func() int {
		var n int
		if err := f.db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tokens WHERE mission_owner = ? AND revoked_at IS NULL`, owner).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if live() == 0 {
		t.Fatal("the child has no live task token to refuse")
	}
	rf, ok := turn.(interface{ Refuse() error })
	if !ok {
		t.Fatalf("%T cannot be refused", turn)
	}
	if err := rf.Refuse(); err != nil {
		t.Fatal(err)
	}
	if syscall.Kill(turn.PID(), 0) != nil {
		t.Fatal("the process is gone: this test needs it alive")
	}
	if live() != 0 {
		t.Error("the refused child's task token is still live while its process runs")
	}
}

// A refused child whose task token cannot be ended says so from Refuse (the error is not swallowed), and the token is
// dead once the revoke works again.
func TestARefuseThatCannotEndAChildsTaskTokenReturnsTheError(t *testing.T) {
	f := newChildFix(t)
	f.mint.revokeBackoff = time.Millisecond
	real := f.mint.tokens.RevokeOwner
	failing := true
	f.mint.revokeOwnerFn = func(ctx context.Context, owner string) (int, error) {
		if failing {
			return 0, errors.New("database is locked")
		}
		return real(ctx, owner)
	}
	dir := t.TempDir()
	bin := fakeChildBin(t, dir)
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(strings.Replace(string(b), "#!/bin/sh\n", "#!/bin/sh\ntrap '' TERM\n", 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	oldGrace := hostTurnGrace
	hostTurnGrace = time.Hour
	t.Cleanup(func() { hostTurnGrace = oldGrace; _ = os.WriteFile(filepath.Join(dir, "release"), []byte("x"), 0o600) })
	store := coderhost.NewStore(f.db.DB)
	l := &execLauncher{bin: bin, dbPath: f.m.dbPath, instance: "host-test", logDir: filepath.Join(dir, "turns"), child: f.mint,
		revoke: store.RevokeSessionTokensErr, revokeTurn: store.RevokeTurnSessionTokensErr, revokeBackoff: time.Millisecond}
	turn, err := l.Start("default", f.child, "master-1", "hi", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	rf := turn.(interface{ Refuse() error })
	rerr := rf.Refuse()
	if rerr == nil || !coderhost.TokensMayBeLive(rerr) || !strings.Contains(rerr.Error(), "could not be ended") {
		t.Fatalf("Refuse with a task token that cannot be ended: %v", rerr)
	}
	owner := dynorg.TurnOwner("default", f.child, "master-1")
	live := func() int {
		var n int
		if err := f.db.DB.QueryRow(`SELECT COUNT(*) FROM agent_tokens WHERE mission_owner = ? AND revoked_at IS NULL`, owner).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if live() == 0 {
		t.Fatal("the task token is already dead although every revoke failed")
	}
	failing = false
	if rerr := rf.Refuse(); rerr != nil { // the retry succeeds: the token is dead
		t.Fatalf("the retry: %v", rerr)
	}
	if live() != 0 {
		t.Error("the task token is still live after the retry")
	}
}
