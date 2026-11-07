package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/orggrant"
)

// brokenDBServer is a grant-mode server whose database cannot be opened: any
// path through a grant-list read or runtime bootstrap fails with a
// non-refusal error, so a refusal that still comes back made no database
// access.
func brokenDBServer(t *testing.T) *Server {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(Options{DBPath: filepath.Join(blocker, "x.db"), Profile: "default", Grant: "g-1"})
	t.Cleanup(func() { s.closeRuntime() })
	return s
}

// The grant-mode refusal of a tool the grant can never serve is decided from
// the name alone: the answer is refused_grant for every environment, every
// grant state and a live or canceled context, and it never reads the database.
func TestGrantModeRefusalIsDecidedBeforeAnyGrantRead(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	ctxs := map[string]context.Context{"live": context.Background(), "canceled": canceled}
	envs := map[string][2]string{"both": {"growth", "writer"}, "no-org": {"", "writer"}, "no-role": {"growth", ""}, "none": {"", ""}}
	notServed := []string{"task_list", "task_next", "api_status", "workflow_list", "doctor", "automation", "no_such_tool", ""}
	for ctxName, ctx := range ctxs {
		for envName, env := range envs {
			for _, name := range notServed {
				t.Run(ctxName+"/"+envName+"/"+name, func(t *testing.T) {
					t.Setenv("MONOMIND_ORG_NAME", env[0])
					t.Setenv("MONOMIND_ORG_ROLE", env[1])
					// grants absent: the database cannot even be opened.
					if _, err := brokenDBServer(t).callGrantTool(ctx, name, nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
						t.Fatalf("grants absent: %q = %v, want a %s refusal", name, err, codeRefusedGrant)
					}
					// grants present
					f := newGrantFixture(t, orggrant.Tool{Wait: true})
					t.Setenv("MONOMIND_ORG_NAME", env[0])
					t.Setenv("MONOMIND_ORG_ROLE", env[1])
					if _, err := f.server.callGrantTool(ctx, name, nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
						t.Fatalf("grants present: %q = %v, want a %s refusal", name, err, codeRefusedGrant)
					}
				})
			}
		}
	}
}

// A grant tool with the environment missing is refused before the database is
// read too, canceled context or not; with the environment present and the
// context canceled the call still fails closed (an error, nothing served).
func TestGrantModeServedNameStaysRefusedOrFailsClosed(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range []string{"automation_publish", "automation_status", "automation_output", "decision_list", "org_start"} {
		t.Run(name, func(t *testing.T) {
			for ctxName, ctx := range map[string]context.Context{"live": context.Background(), "canceled": canceled} {
				for envName, env := range map[string][2]string{"no-org": {"", "writer"}, "no-role": {"growth", ""}} {
					t.Setenv("MONOMIND_ORG_NAME", env[0])
					t.Setenv("MONOMIND_ORG_ROLE", env[1])
					if _, err := brokenDBServer(t).callGrantTool(ctx, name, nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
						t.Errorf("%s/%s %s: %v, want a %s refusal without a database", ctxName, envName, name, err, codeRefusedGrant)
					}
				}
			}
			f := newGrantFixture(t, orggrant.Tool{Wait: true})
			if out, err := f.server.callGrantTool(canceled, name, nil); err == nil {
				t.Errorf("canceled context served %s: %s", name, out)
			}
		})
	}
}

func refusalRows(t *testing.T, f *grantFixture) int {
	t.Helper()
	f.server.refusals.wait()
	var n int
	if err := f.db.DB.QueryRow(`SELECT COUNT(*) FROM org_bridge_calls WHERE direction = 'role_tool' AND status = 'refused_grant' AND grant_id = ? AND org_name = 'growth' AND role_id = 'writer'`, f.grant.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func slowLedger(t *testing.T) {
	t.Helper()
	old := grantRefusalLedgerTimeout
	grantRefusalLedgerTimeout = 10 * time.Second
	t.Cleanup(func() { grantRefusalLedgerTimeout = old })
}

func refuseOnce(t *testing.T, s *Server, ctx context.Context, name string) {
	t.Helper()
	if _, err := s.callGrantTool(ctx, name, nil); err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) {
		t.Fatalf("%q = %v, want a %s refusal", name, err, codeRefusedGrant)
	}
}

// A never-served name still leaves its refusal row in the ledger, written
// after the refusal is decided and on its own context, so a canceled request
// context does not lose it.
func TestGrantModeNeverServedNameLeavesALedgerRow(t *testing.T) {
	slowLedger(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for ctxName, ctx := range map[string]context.Context{"live": context.Background(), "canceled": canceled} {
		t.Run(ctxName, func(t *testing.T) {
			f := newGrantFixture(t, orggrant.Tool{Wait: true})
			refuseOnce(t, f.server, ctx, "task_list")
			if n := refusalRows(t, f); n != 1 {
				t.Fatalf("%d refusal rows, want 1", n)
			}
		})
	}
}

// The first refusal per name per minute is recorded, on a fake clock; a flood
// of distinct names stays bounded.
func TestGrantModeRefusalRowsAreLimitedPerNamePerMinute(t *testing.T) {
	slowLedger(t)
	now := time.Now()
	old := grantRefusalNow
	grantRefusalNow = func() time.Time { return now }
	t.Cleanup(func() { grantRefusalNow = old })
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	for i := 0; i < 5; i++ {
		refuseOnce(t, f.server, context.Background(), "task_list")
		f.server.refusals.wait()
	}
	if n := refusalRows(t, f); n != 1 {
		t.Fatalf("%d rows after 5 refusals of one name in a minute, want 1", n)
	}
	refuseOnce(t, f.server, context.Background(), "task_next")
	if n := refusalRows(t, f); n != 2 {
		t.Fatalf("%d rows after a second name, want 2", n)
	}
	now = now.Add(61 * time.Second)
	refuseOnce(t, f.server, context.Background(), "task_list")
	if n := refusalRows(t, f); n != 3 {
		t.Fatalf("%d rows a minute later, want 3", n)
	}
	oldMax := grantRefusalMaxNames
	grantRefusalMaxNames = 4
	t.Cleanup(func() { grantRefusalMaxNames = oldMax })
	for i := 0; i < 12; i++ {
		refuseOnce(t, f.server, context.Background(), "flood_"+strconv.Itoa(i))
		f.server.refusals.wait()
	}
	f.server.refusals.mu.Lock()
	remembered := len(f.server.refusals.last)
	f.server.refusals.mu.Unlock()
	if remembered > 4 {
		t.Fatalf("%d names remembered, cap is 4", remembered)
	}
	if n := refusalRows(t, f); n > 3+4 { // the three earlier rows, then at most the cap
		t.Fatalf("%d rows after a flood of distinct names, want at most 7", n)
	}
}

// A process whose environment does not match the stored grant writes no row,
// and a refusal is still returned.
func TestGrantModeMismatchedEnvironmentWritesNoRefusalRow(t *testing.T) {
	slowLedger(t)
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	t.Setenv("MONOMIND_ORG_NAME", "other")
	refuseOnce(t, f.server, context.Background(), "task_list")
	f.server.refusals.wait()
	t.Setenv("MONOMIND_ORG_NAME", "growth")
	t.Setenv("MONOMIND_ORG_ROLE", "other")
	refuseOnce(t, f.server, context.Background(), "task_next")
	if n := refusalRows(t, f); n != 0 {
		t.Fatalf("%d rows written for a mismatched environment", n)
	}
}

// The refusal is not held by the ledger write: it returns at once while the
// insert is blocked, and the write itself gives up within its timeout.
func TestGrantModeRefusalIsNotHeldByTheLedgerWrite(t *testing.T) {
	old := grantRefusalLedgerTimeout
	grantRefusalLedgerTimeout = 100 * time.Millisecond
	t.Cleanup(func() { grantRefusalLedgerTimeout = old })
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	tx, err := f.db.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.Exec(`UPDATE org_bridge_calls SET status = status`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	refuseOnce(t, f.server, context.Background(), "task_list")
	returned := time.Since(start)
	f.server.refusals.wait()
	if returned > 50*time.Millisecond {
		t.Fatalf("refusal took %v with the ledger write blocked", returned)
	}
	if total := time.Since(start); total > 5*time.Second {
		t.Fatalf("the ledger write outlived its timeout: %v", total)
	}
}

// Names that only look like a grant tool are never served.
func TestGrantModeNearMissNamesAreRefused(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, name := range []string{"Automation_x", "AUTOMATION_PUBLISH", "automation_", "automation", " automation_x", "\u0430utomation_x", "automation\u2010x", "automation-x", "decision_list ", "Decision_list", "org_"} {
		if grantServesName(name) {
			t.Errorf("grantServesName(%q) = true", name)
		}
		refuseOnce(t, f.server, canceled, name)
	}
}

// An unset environment with an unknown grant gives the environment refusal
// (the environment is checked before the grant is read).
func TestGrantModeUnsetEnvironmentBeatsUnknownGrant(t *testing.T) {
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	f.server.opts.Grant = "no-such-grant"
	t.Setenv("MONOMIND_ORG_NAME", "")
	_, err := f.server.callGrantTool(context.Background(), "automation_publish", nil)
	if err == nil || !strings.HasPrefix(err.Error(), codeRefusedGrant) || !strings.Contains(err.Error(), "MONOMIND_ORG_NAME") {
		t.Fatalf("got %v, want the MONOMIND_ORG_NAME refusal", err)
	}
}

// stall makes the abandonable work block until release() at the given hook,
// reporting when it got there.
func stall(t *testing.T, hook *func()) (entered <-chan struct{}, release func()) {
	t.Helper()
	in, gate := make(chan struct{}, 8), make(chan struct{})
	*hook = func() { in <- struct{}{}; <-gate }
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() { release(); *hook = nil })
	return in, release
}

func shortLedger(t *testing.T, d time.Duration) {
	t.Helper()
	old := grantRefusalLedgerTimeout
	grantRefusalLedgerTimeout = d
	t.Cleanup(func() { grantRefusalLedgerTimeout = old })
}

func runtimeBuilt(s *Server) bool {
	s.rtMu.Lock()
	defer s.rtMu.Unlock()
	return s.rt != nil
}

// Work that outlives its deadline and the server's close builds no runtime.
func TestGrantModeAbandonedRefusalWorkBuildsNoRuntimeAfterClose(t *testing.T) {
	shortLedger(t, 20*time.Millisecond)
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	f.server.closeRuntime() // the fixture may have built none; start closed
	entered, release := stall(t, &grantRefusalBeforeScope)
	refuseOnce(t, f.server, context.Background(), "task_list")
	<-entered
	f.server.refusals.wait() // the deadline passes with the work still stalled
	f.server.closeRuntime()
	release()
	f.server.refusals.work.Wait()
	if runtimeBuilt(f.server) {
		t.Fatal("abandoned work rebuilt the runtime after closeRuntime")
	}
}

// A stalled write that returns after closeRuntime closed the database neither
// panics nor writes.
func TestGrantModeAbandonedRefusalWorkSurvivesAClosedDatabase(t *testing.T) {
	shortLedger(t, 500*time.Millisecond)
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	if _, err := f.server.runtime(); err != nil { // built up front: the scope read is then quick
		t.Fatal(err)
	}
	entered, release := stall(t, &grantRefusalAfterScope)
	refuseOnce(t, f.server, context.Background(), "task_list")
	<-entered
	f.server.refusals.wait()
	f.server.closeRuntime()
	release()
	f.server.refusals.work.Wait() // a panic here would fail the run
	var n int
	if err := f.db.DB.QueryRow(`SELECT COUNT(*) FROM org_bridge_calls WHERE status = 'refused_grant'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d rows written through a closed runtime", n)
	}
}

// One write is in flight at a time: a second refusal while the first is
// stalled starts no second one.
func TestGrantModeRefusalRowsAreSingleFlight(t *testing.T) {
	shortLedger(t, 10*time.Second)
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	var started int32
	entered, release := stall(t, &grantRefusalBeforeScope)
	inner := grantRefusalBeforeScope
	grantRefusalBeforeScope = func() { atomic.AddInt32(&started, 1); inner() }
	refuseOnce(t, f.server, context.Background(), "task_list")
	<-entered
	refuseOnce(t, f.server, context.Background(), "task_next")
	refuseOnce(t, f.server, context.Background(), "api_status")
	if _, ok := f.server.refusals.admit(f.server, "another_name"); ok {
		f.server.refusals.release()
		t.Fatal("a second write slot was handed out while one write was in flight")
	}
	if n := atomic.LoadInt32(&started); n != 1 {
		t.Fatalf("%d writes started while one was in flight, want 1", n)
	}
	release()
	f.server.refusals.wait()
}

// closeRuntime waits for the write that holds the slot, up to its deadline.
func TestGrantModeCloseRuntimeWaitsForTheRefusalWrite(t *testing.T) {
	shortLedger(t, 300*time.Millisecond)
	f := newGrantFixture(t, orggrant.Tool{Wait: true})
	entered, release := stall(t, &grantRefusalBeforeScope)
	refuseOnce(t, f.server, context.Background(), "task_list")
	<-entered
	start := time.Now()
	f.server.closeRuntime()
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Fatalf("closeRuntime returned after %v without waiting for the write", d)
	}
	release()
	f.server.refusals.work.Wait()
}

// A name whose write failed is not remembered, so the next refusal retries.
func TestGrantModeFailedRefusalWriteIsNotRemembered(t *testing.T) {
	s := brokenDBServer(t)
	t.Setenv("MONOMIND_ORG_NAME", "growth")
	t.Setenv("MONOMIND_ORG_ROLE", "writer")
	refuseOnce(t, s, context.Background(), "task_list")
	s.refusals.wait()
	s.refusals.work.Wait()
	s.refusals.mu.Lock()
	n := len(s.refusals.last)
	s.refusals.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d names remembered after a failed write", n)
	}
}

// Failed attempts (the scope check fails fast: wrong profile, bad environment,
// missing grant, database error) are rate limited too: after one fails, no
// further attempt opens the database until the back-off passes.
func TestGrantModeFailedRefusalAttemptsAreBackedOff(t *testing.T) {
	now := time.Now()
	oldNow := grantRefusalNow
	grantRefusalNow = func() time.Time { return now }
	t.Cleanup(func() { grantRefusalNow = oldNow })
	var attempts int32
	grantRefusalBeforeScope = func() { atomic.AddInt32(&attempts, 1) }
	t.Cleanup(func() { grantRefusalBeforeScope = nil })
	s := brokenDBServer(t)
	t.Setenv("MONOMIND_ORG_NAME", "growth")
	t.Setenv("MONOMIND_ORG_ROLE", "writer")
	for i := 0; i < 6; i++ {
		refuseOnce(t, s, context.Background(), "flood_"+strconv.Itoa(i))
		s.refusals.wait()
		s.refusals.work.Wait()
	}
	if n := atomic.LoadInt32(&attempts); n != 1 {
		t.Fatalf("%d attempts in a burst of failing refusals, want 1", n)
	}
	now = now.Add(grantRefusalFailBackoff + time.Second)
	refuseOnce(t, s, context.Background(), "after")
	s.refusals.wait()
	s.refusals.work.Wait()
	if n := atomic.LoadInt32(&attempts); n != 2 {
		t.Fatalf("%d attempts after the back-off, want 2", n)
	}
}

// An admitted attempt carries its epoch and test hooks as they were at
// admission: a later change of the globals or of the epoch does not reach it.
func TestGrantModeAdmittedRefusalCapturesEpochAndHooks(t *testing.T) {
	s := brokenDBServer(t)
	s.rtMu.Lock()
	s.epoch = 7
	s.rtMu.Unlock()
	var first int32
	grantRefusalBeforeScope = func() { atomic.AddInt32(&first, 1) }
	t.Cleanup(func() { grantRefusalBeforeScope, grantRefusalAfterScope = nil, nil })
	slot, ok := s.refusals.admit(s, "task_list")
	if !ok {
		t.Fatal("not admitted")
	}
	defer s.refusals.release()
	s.rtMu.Lock()
	s.epoch = 8
	s.rtMu.Unlock()
	grantRefusalBeforeScope = nil
	if slot.epoch != 7 {
		t.Fatalf("slot epoch %d, want the admission-time 7", slot.epoch)
	}
	if slot.beforeScope == nil {
		t.Fatal("slot lost the hook set at admission")
	}
	slot.beforeScope()
	if atomic.LoadInt32(&first) != 1 {
		t.Fatal("slot hook is not the one set at admission")
	}
}
