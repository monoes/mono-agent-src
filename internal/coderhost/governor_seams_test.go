package coderhost

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// The live policy can only lower the limit a tree has always had.
func TestLivePolicyOnlyLowersTheLiveSessionLimit(t *testing.T) {
	st := newStoreT(t)
	for _, c := range []struct{ policy, want int }{{0, MaxLiveSessionsPerTree}, {-4, MaxLiveSessionsPerTree}, {3, 3}, {MaxLiveSessionsPerTree, MaxLiveSessionsPerTree}, {1000, MaxLiveSessionsPerTree}} {
		st.SetLivePolicy(func(string, string) int { return c.policy })
		if got := st.liveCeiling("p", "c"); got != c.want {
			t.Errorf("policy %d: limit %d, want %d", c.policy, got, c.want)
		}
	}
	st.SetLivePolicy(nil)
	if st.liveCeiling("p", "c") != MaxLiveSessionsPerTree {
		t.Fatal("no policy must be today's limit")
	}
}

func TestLivePolicyLowersTheSpawnLimitOfATree(t *testing.T) {
	st := newStoreT(t)
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return now }
	st.SetLivePolicy(func(string, string) int { return 2 })
	root, _ := master(t, st, "coding")
	s, _ := st.GetSettings("p", root.ConversationID)
	s.MaxChildren = 20
	if err := st.SaveSettings("p", root.ConversationID, s); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		now = now.Add(SpawnRateWindow + time.Minute)
		spawn(t, st, root)
	}
	now = now.Add(SpawnRateWindow + time.Minute)
	if _, err := st.Spawn(root, SpawnRequest{}); !errors.Is(err, ErrLimit) {
		t.Fatalf("a third live session under a policy of 2: %v", err)
	}
	if n, _ := st.liveInTree("p", root.ConversationID); n != 3 {
		t.Fatalf("a refused spawn left %d sessions", n)
	}
}

// A launcher that defers leaves the send pending and in order: nothing fails, nothing starts, the session is idle, and the
// same send starts when the launcher has room.
func TestADeferredLaunchKeepsTheSendPendingAndStartsItLater(t *testing.T) {
	h, l, st := newHost(t)
	l.startErr = fmt.Errorf("no room: %w", ErrDeferred)
	c1 := send(t, st, "c1", "t1", "first")
	c2 := send(t, st, "c1", "t2", "second")
	_ = h.Tick()
	for _, c := range []Command{c1, c2} {
		got, _, _ := st.GetCommand(c.ID)
		if got.Status != CmdPending {
			t.Fatalf("a deferred send is %v (error %q), want pending", got.Status, got.Error)
		}
	}
	if x := state(t, st, "c1"); x.State != StateIdle || x.TurnID != "" {
		t.Fatalf("a deferred send left the session %v turn %q", x.State, x.TurnID)
	}
	if len(l.started) != 0 {
		t.Fatalf("started %v while deferred", l.started)
	}
	l.mu.Lock()
	l.startErr = nil
	l.mu.Unlock()
	_ = h.Tick()
	l.mu.Lock()
	started := append([]string(nil), l.started...)
	l.mu.Unlock()
	if len(started) != 1 || started[0] != "t1:first" {
		t.Fatalf("after room, started %v: the first send must go first, alone (one turn per session)", started)
	}
}

type deferOne struct {
	*fakeLauncher
	turn string
	on   bool
}

func (d *deferOne) Start(p, c, turn, m string, a []string, tok string) (Turn, error) {
	if d.on && turn == d.turn {
		return nil, fmt.Errorf("no room: %w", ErrDeferred)
	}
	return d.fakeLauncher.Start(p, c, turn, m, a, tok)
}

// A deferred send keeps the later sends of the same chat behind it: they do not start first in the same pass.
func TestADeferredSendKeepsTheLaterSendsOfTheChatInOrder(t *testing.T) {
	h, l, st := newHost(t)
	d := &deferOne{fakeLauncher: l, turn: "t1", on: true}
	h.L = d
	send(t, st, "c1", "t1", "first")
	send(t, st, "c1", "t2", "second")
	_ = h.Tick()
	l.mu.Lock()
	n := len(l.started)
	l.mu.Unlock()
	if n != 0 {
		t.Fatalf("started %v: the second send overtook the deferred first one", l.started)
	}
}
