package slots

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

func TestReconcileFreesOrphansAndNeverALiveMembersSlot(t *testing.T) {
	e := newEnv(t)
	e.register(t, "sess-t1-1") // live member with a session (2 slots)
	e.register(t, "proc-1")    // live member with a process slot
	for _, r := range []governor.Request{sess("t1", 1), proc(1), sess("t1", 2), proc(2)} {
		if err := e.s.Reserve(ctx, e.l, capProcess, r); err != nil {
			t.Fatal(err)
		}
	}
	// sess-t1-2 and proc-2 were never registered: their hosts crashed.
	n, err := e.s.Reconcile(ctx, "p", 1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 { // sess-t1-2 holds two slots, proc-2 one
		t.Fatalf("freed %d, want 3", n)
	}
	if p, s := e.live(t, governor.KindProcess, ""), e.live(t, governor.KindSession, ""); p != 2 || s != 1 {
		t.Fatalf("live member slots must survive: process=%d session=%d", p, s)
	}
	if n, _ := e.s.Reconcile(ctx, "p", 1); n != 0 {
		t.Fatalf("second reconcile freed %d", n)
	}
	// The freed capacity is reusable (a leaked slot does not starve the run).
	if err := e.s.Reserve(ctx, e.l, capProcess, sess("t1", 3)); err != nil {
		t.Fatalf("capacity after reconcile: %v", err)
	}
}

func TestReconcileReleasedMemberIsAnOrphan(t *testing.T) {
	e := newEnv(t)
	e.register(t, "proc-1")
	if err := e.s.Reserve(ctx, e.l, capProcess, proc(1)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Release(ctx, "p", 1, "proc-1"); err != nil {
		t.Fatal(err)
	}
	if n, err := e.s.Reconcile(ctx, "p", 1); err != nil || n != 1 {
		t.Fatalf("freed %d, err %v", n, err)
	}
}

func TestReconcileFreesNothingWhenTheRegistryCannotBeRead(t *testing.T) {
	e := newEnv(t)
	e.register(t, "proc-1")
	if err := e.s.Reserve(ctx, e.l, capProcess, proc(1)); err != nil {
		t.Fatal(err)
	}
	e.s.view = registry{st: e.st, err: errors.New("registry unreadable")}
	if n, err := e.s.Reconcile(ctx, "p", 1); err == nil || n != 0 {
		t.Fatalf("freed %d, err %v", n, err)
	}
	if e.live(t, governor.KindProcess, "") != 1 {
		t.Fatal("a registry failure freed a slot")
	}
}

func TestReconcileLeavesSlotsReservedAfterTheRegistryRead(t *testing.T) {
	e := newEnv(t)
	hook := registry{st: e.st}
	e.s.view = viewFunc(func() (map[string]bool, error) {
		// A reservation lands after Reconcile fixed the slot ids it judges; its member is not in the set read.
		if err := e.s.Reserve(ctx, e.l, capProcess, proc(5)); err != nil {
			t.Error(err)
		}
		return hook.LiveMemberIDs(ctx, "p", 1)
	})
	if err := e.s.Reserve(ctx, e.l, capProcess, proc(1)); err != nil {
		t.Fatal(err)
	}
	if n, err := e.s.Reconcile(ctx, "p", 1); err != nil || n != 1 {
		t.Fatalf("freed %d, err %v (only the older orphan)", n, err)
	}
	if e.live(t, governor.KindProcess, "") != 1 {
		t.Fatal("the slot reserved during the read was freed")
	}
}

type viewFunc func() (map[string]bool, error)

func (f viewFunc) Running(_ context.Context, _ string, _ int64) (bool, error) { return true, nil }
func (f viewFunc) LiveMemberIDs(_ context.Context, _ string, _ int64) (map[string]bool, error) {
	return f()
}
