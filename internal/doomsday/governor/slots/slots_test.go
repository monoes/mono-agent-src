package slots

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

func TestSessionTakesProcessAndSessionSlot(t *testing.T) {
	e := newEnv(t)
	if err := e.s.Reserve(ctx, e.l, 3, sess("t1", 1)); err != nil {
		t.Fatal(err)
	}
	if p, s := e.live(t, governor.KindProcess, ""), e.live(t, governor.KindSession, ""); p != 1 || s != 1 {
		t.Fatalf("a session takes one of each, got process=%d session=%d", p, s)
	}
}

func TestToolProcessAndRootTakeProcessOnly(t *testing.T) {
	e := newEnv(t)
	if err := e.s.Reserve(ctx, e.l, 3, proc(1)); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Reserve(ctx, e.l, 3, governor.Request{Kind: governor.KindProcess, Tree: "t1", MemberID: "tool"}); err != nil {
		t.Fatal(err)
	}
	if p, s := e.live(t, governor.KindProcess, ""), e.live(t, governor.KindSession, ""); p != 2 || s != 0 {
		t.Fatalf("process=%d session=%d", p, s)
	}
	if n := e.live(t, governor.KindProcess, "t1"); n != 1 {
		t.Fatalf("per-tree process count = %d", n)
	}
}

func TestProcessLimitIsExactlyTheLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 2; i++ {
		if err := e.s.Reserve(ctx, e.l, 2, proc(i)); err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
	}
	if err := e.s.Reserve(ctx, e.l, 2, proc(9)); !errors.Is(err, governor.ErrNoSlot) {
		t.Fatalf("over the effective limit: %v", err)
	}
	if n := e.live(t, governor.KindProcess, ""); n != 2 {
		t.Fatalf("a refusal must write nothing, live=%d", n)
	}
}

func TestEffectiveLimitNeverRaisesAboveCharterCap(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < capProcess; i++ {
		if err := e.s.Reserve(ctx, e.l, 1000, proc(i)); err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
	}
	if err := e.s.Reserve(ctx, e.l, 1000, proc(99)); !errors.Is(err, governor.ErrNoSlot) {
		t.Fatalf("a limit above the charter cap must not admit past it: %v", err)
	}
	// Even a caller passing a raised Limits.MaxConcurrent is held by the migration's trigger on the charter's number.
	raised := e.l
	raised.MaxConcurrent = 1000
	if err := e.s.Reserve(ctx, raised, 1000, proc(98)); !errors.Is(err, governor.ErrNoSlot) {
		t.Fatalf("the store refuses above the charter regardless: %v", err)
	}
}

func TestNonPositiveLimitAdmitsNothing(t *testing.T) {
	e := newEnv(t)
	for _, lim := range []int{0, -1} {
		if err := e.s.Reserve(ctx, e.l, lim, proc(1)); !errors.Is(err, governor.ErrNoSlot) {
			t.Fatalf("limit %d: %v", lim, err)
		}
	}
}

func TestLivePerTreeIsIndependentOfProcessCap(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < capTree; i++ {
		if err := e.s.Reserve(ctx, e.l, capProcess, sess("t1", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.s.Reserve(ctx, e.l, capProcess, sess("t1", 9)); !errors.Is(err, governor.ErrNoSlot) {
		t.Fatalf("tree is full: %v", err)
	}
	if n := e.live(t, governor.KindProcess, ""); n != capTree {
		t.Fatalf("the refused session left a process slot behind: %d", n)
	}
	if err := e.s.Reserve(ctx, e.l, capProcess, sess("t2", 1)); err != nil {
		t.Fatalf("another tree still has room: %v", err)
	}
	// The process cap (4) is now full: a session in a roomy tree is refused on the process slot.
	if err := e.s.Reserve(ctx, e.l, capProcess, proc(1)); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Reserve(ctx, e.l, capProcess, sess("t3", 1)); !errors.Is(err, governor.ErrNoSlot) {
		t.Fatalf("process cap reached: %v", err)
	}
}

func TestTreeCapAppliesPerTreeOnTheTriggerToo(t *testing.T) {
	e := newEnv(t)
	raised := e.l
	raised.LivePerTree = 1000
	for i := 0; i < capTree; i++ {
		if err := e.s.Reserve(ctx, raised, capProcess, sess("t1", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.s.Reserve(ctx, raised, capProcess, sess("t1", 9)); !errors.Is(err, governor.ErrNoSlot) {
		t.Fatalf("charter live_per_tree holds under a raised caller value: %v", err)
	}
}

func TestOneMemberHoldsSlotsOnce(t *testing.T) {
	e := newEnv(t)
	if err := e.s.Reserve(ctx, e.l, 3, sess("t1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Reserve(ctx, e.l, 3, sess("t1", 1)); !errors.Is(err, governor.ErrAlreadyHeld) {
		t.Fatalf("second reserve for the same member: %v", err)
	}
	if e.live(t, governor.KindProcess, "") != 1 {
		t.Fatal("a refused duplicate must not take anything")
	}
}

func TestInvalidRequestsAreRefused(t *testing.T) {
	e := newEnv(t)
	for name, r := range map[string]governor.Request{
		"no member":       {Kind: governor.KindProcess},
		"unknown kind":    {Kind: "gpu", MemberID: "m"},
		"session no tree": {Kind: governor.KindSession, MemberID: "m"},
	} {
		if err := e.s.Reserve(ctx, e.l, 3, r); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestReleaseIsIdempotentAndFreesTheSlots(t *testing.T) {
	e := newEnv(t)
	r := sess("t1", 1)
	if err := e.s.Reserve(ctx, e.l, 3, r); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := e.s.Release(ctx, "p", 1, r.MemberID); err != nil {
			t.Fatalf("release %d: %v", i, err)
		}
	}
	if err := e.s.Release(ctx, "p", 1, "never-held"); err != nil {
		t.Fatalf("a member without slots: %v", err)
	}
	if e.live(t, governor.KindProcess, "")+e.live(t, governor.KindSession, "") != 0 {
		t.Fatal("slots still live after release")
	}
	if err := e.s.Reserve(ctx, e.l, 3, r); err != nil {
		t.Fatalf("a released member can reserve again: %v", err)
	}
}

func TestReserveRefusedAfterStopButReleaseWorks(t *testing.T) {
	e := newEnv(t)
	if err := e.s.Reserve(ctx, e.l, 3, proc(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Advance(ctx, "p", 1, store.StateStopRequested, "op", "test"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Reserve(ctx, e.l, 3, proc(2)); !errors.Is(err, governor.ErrNotRunning) {
		t.Fatalf("after Stop: %v", err)
	}
	if err := e.s.Reserve(ctx, e.l, 3, sess("t1", 1)); !errors.Is(err, governor.ErrNotRunning) {
		t.Fatalf("session after Stop: %v", err)
	}
	if err := e.s.Release(ctx, "p", 1, "proc-1"); err != nil {
		t.Fatalf("release must work on a stopped run: %v", err)
	}
	if e.live(t, governor.KindProcess, "") != 0 {
		t.Fatal("slot not released after Stop")
	}
}

func TestReserveForUnknownRunIsNotRunning(t *testing.T) {
	e := newEnv(t)
	l := e.l
	l.Generation = 7
	if err := e.s.Reserve(ctx, l, 3, proc(1)); !errors.Is(err, governor.ErrNotRunning) {
		t.Fatalf("no such run: %v", err)
	}
}

func TestTriggerBacksUpTheNotRunningRule(t *testing.T) {
	e := newEnv(t)
	if _, err := e.st.Advance(ctx, "p", 1, store.StateStopRequested, "op", "test"); err != nil {
		t.Fatal(err)
	}
	// A writer that skips Reserve is still refused by the migration, and the refusal maps to the typed error.
	_, err := e.db.ExecContext(ctx, `INSERT INTO doomsday_slots(profile, generation, kind, tree, member_id, created_at) VALUES ('p',1,'process','-','x',1)`)
	if !errors.Is(mapTrigger(err), governor.ErrNotRunning) {
		t.Fatalf("trigger refusal: %v", err)
	}
}

func TestCrashMidReserveLeavesNoHalfHeldSession(t *testing.T) {
	e := newEnv(t)
	e.s.afterProcessInsert = func() error { return errors.New("injected failure between the two inserts") }
	if err := e.s.Reserve(ctx, e.l, 3, sess("t1", 1)); err == nil {
		t.Fatal("expected the injected failure")
	}
	e.s.afterProcessInsert = nil
	if p, s := e.live(t, governor.KindProcess, ""), e.live(t, governor.KindSession, ""); p != 0 || s != 0 {
		t.Fatalf("half-held session after a failed reservation: process=%d session=%d", p, s)
	}
	if err := e.s.Reserve(ctx, e.l, 3, sess("t1", 1)); err != nil {
		t.Fatalf("the member can reserve after the failure (connection not left inside a transaction): %v", err)
	}
}

func TestCancelledContextLeavesNoSlotAndNoOpenTransaction(t *testing.T) {
	e := newEnv(t)
	c, cancel := context.WithCancel(ctx)
	cancel()
	if err := e.s.Reserve(c, e.l, 3, sess("t1", 1)); err == nil {
		t.Fatal("cancelled context must fail")
	}
	for i := 0; i < 5; i++ { // every pooled connection is usable afterwards
		if err := e.s.Reserve(ctx, e.l, 3, proc(i)); err != nil {
			t.Fatalf("reserve %d after a cancelled one: %v", i, err)
		}
		_ = e.s.Release(ctx, "p", 1, proc(i).MemberID)
	}
}
