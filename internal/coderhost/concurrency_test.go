package coderhost

import (
	"strings"
	"testing"
)

func startedConvs(l *fakeLauncher) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.started...)
}

func setMaxConcurrent(t *testing.T, st *Store, conv string, n int) {
	t.Helper()
	s, err := st.GetSettings("p", conv)
	if err != nil {
		t.Fatal(err)
	}
	s.MaxConcurrent = n
	if err := st.SaveSettings("p", conv, s); err != nil {
		t.Fatal(err)
	}
}

func TestMaxConcurrentHoldsExtraChildSendsInOrderAndReleasesThemAsTurnsEnd(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 4)
	setMaxConcurrent(t, st, "m", 2)
	for i, k := range kids {
		send(t, st, k, "t"+string(rune('1'+i)), "work "+k)
	}
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if got := startedConvs(l); len(got) != 2 || !strings.HasPrefix(got[0], "t1:") || !strings.HasPrefix(got[1], "t2:") {
		t.Fatalf("started %q: want the first two sends only, in order", got)
	}
	for _, k := range kids[2:] {
		if x := state(t, st, k); x.State == StateRunning || x.TurnID != "" {
			t.Fatalf("%s runs a turn beyond max_concurrent: %+v", k, x)
		}
	}
	// A second pass changes nothing while both slots are taken.
	_ = h.Tick()
	if len(startedConvs(l)) != 2 {
		t.Fatalf("started %q", startedConvs(l))
	}
	// One turn ends: exactly the next send in order takes the slot.
	l.turn("t1").end()
	waitFor(t, "t1 to settle", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.live[key("p", kids[0])] == nil })
	_ = h.Tick()
	if got := startedConvs(l); len(got) != 3 || !strings.HasPrefix(got[2], "t3:") {
		t.Fatalf("started %q: want t3 next", got)
	}
	l.turn("t2").end()
	l.turn("t3").end()
	waitFor(t, "t2 and t3 to settle", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.live[key("p", kids[1])] == nil && h.live[key("p", kids[2])] == nil
	})
	_ = h.Tick()
	if got := startedConvs(l); len(got) != 4 || !strings.HasPrefix(got[3], "t4:") {
		t.Fatalf("started %q: want t4 last", got)
	}
}

func TestMaxConcurrentIsPerMasterAndDoesNotHoldMastersOrSoloSessions(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 2)
	setMaxConcurrent(t, st, "m", 1)
	// a second family and a solo session are counted apart
	addConversation(t, st, "m2", "/work/m2")
	_ = st.SetRole("p", "m2", "master", "")
	addConversation(t, st, "o1", "/work/o1")
	_ = st.SetRole("p", "o1", "child", "m2")
	addConversation(t, st, "solo", "/work/solo")
	send(t, st, kids[0], "a1", "x")
	send(t, st, kids[1], "a2", "x")
	send(t, st, "o1", "b1", "x")
	send(t, st, "solo", "c1", "x")
	send(t, st, "m", "d1", "x") // the master's own turn is not a child turn
	_ = h.Tick()
	got := strings.Join(startedConvs(l), " ")
	for _, want := range []string{"a1:", "b1:", "c1:", "d1:"} {
		if !strings.Contains(got, want) {
			t.Errorf("%s did not start: %q", want, got)
		}
	}
	if strings.Contains(got, "a2:") {
		t.Errorf("the second child of m started beyond max_concurrent 1: %q", got)
	}
}

func TestMaxConcurrentHoldsAPersonsSendToAChildToo(t *testing.T) {
	h, l, st := newHost(t)
	_, kids := family(t, st, 2)
	setMaxConcurrent(t, st, "m", 1)
	send(t, st, kids[0], "p1", "x")
	_ = h.Tick()
	send(t, st, kids[1], "p2", "typed by a person")
	_ = h.Tick()
	if len(startedConvs(l)) != 1 {
		t.Fatalf("started %q", startedConvs(l))
	}
	if cmds, _ := st.Pending(); len(cmds) != 1 || cmds[0].TurnID != "p2" {
		t.Fatalf("the held send is not pending: %+v", cmds)
	}
}

func TestMaxConcurrentValidationAndDefault(t *testing.T) {
	if DefaultSettings().MaxConcurrent != 2 {
		t.Fatalf("default = %d", DefaultSettings().MaxConcurrent)
	}
	s := DefaultSettings()
	for _, n := range []int{0, 11} {
		s.MaxConcurrent = n
		if s.Validate() == nil {
			t.Errorf("max_concurrent %d accepted", n)
		}
	}
}
