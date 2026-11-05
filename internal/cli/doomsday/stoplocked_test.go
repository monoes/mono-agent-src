package doomsday

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// STOP while another connection holds the database's write lock, with a run open. What the code does: the stop marker file
// is written first (it needs no database), then each database step (the intent, the token revocation) waits for the driver's
// busy timeout (5 s, retried 5 times for the intent) and fails; a failed step is recorded and never rounded up. The command
// therefore returns after a bounded wait, says "NOT RECORDED" for the intent, exits non-zero and never prints "stopped", and
// the run stays open and not quiescent. (Needs the real pinned connection: Linux with a working inotify.)
func TestStopDuringALockedDatabaseIsBoundedAndNeverSaysStopped(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)

	other, err := sql.Open("sqlite", "file:"+w.w.Witness.DBPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	locked := true
	unlock := func() {
		if locked {
			locked = false
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			conn.Close()
			other.Close()
		}
	}
	defer unlock()

	begin := time.Now()
	out, err := run(t, w.deps, "", "stop")
	if d := time.Since(begin); d > 90*time.Second {
		t.Fatalf("stop took %v with the database write-locked: it must be bounded", d)
	}
	// current wording/behaviour, changes in the follow-up PR (busy-database wording, early Stop progress line, lazy Engine)
	if err == nil || !strings.Contains(out, "Stop intent NOT RECORDED in the database") || strings.HasSuffix(out, "stopped\n") {
		t.Fatalf("stop under a locked database reported a clean stop it did not make: %v %q", err, out)
	}
	// The marker is written before the database is touched, so it exists although the intent could not be recorded (a regression
	// that fails the marker instead of the intent would print another line).
	if _, serr := os.Stat(w.w.RunDir.StopPath(1)); serr != nil {
		t.Fatalf("no stop marker in the run dir after a stop whose intent was not recorded: %v", serr)
	}
	unlock()
	runs, rerr := w.w.Store.OpenRuns(context.Background(), "p1")
	if rerr != nil || len(runs) != 1 || runs[0].State == store.StateQuiescent || runs[0].State == store.StateAuthorityRevoked {
		t.Fatalf("the run must stay open and unrevoked after a stop that could not be recorded: %+v %v", runs, rerr)
	}
}
