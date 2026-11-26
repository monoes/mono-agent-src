package chat

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// exhaust takes the rig's approved mission past its $5 budget.
func exhaust(t *testing.T, r leadSpendRig, usd float64) {
	t.Helper()
	if _, err := r.store.AddSessionSpend(context.Background(), "default", r.session, usd, "workers", leadOperator); err != nil {
		t.Fatal(err)
	}
}

func (r leadSpendRig) wrapUp(t *testing.T) *tasks.WrapUp {
	t.Helper()
	w, err := r.store.WrapUpOf(context.Background(), "default", r.session, leadOperator)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

// execs is the argument line of every `agent exec` the fake runtime was started with.
func (r leadSpendRig) execs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(r.argsLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "agent exec ") {
			out = append(out, line)
		}
	}
	return out
}

func (r leadSpendRig) systemPrompt(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(r.systemFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantAllowance(t *testing.T, r leadSpendRig, when string, meter, used, left float64) {
	t.Helper()
	got, _ := r.meter()
	w := r.wrapUp(t)
	if w == nil || math.Abs(got-meter) > 1e-9 || math.Abs(w.SpentUSD-used) > 1e-9 || math.Abs(w.LeftUSD-left) > 1e-9 {
		t.Errorf("after %s: meter $%v, allowance %+v; want meter $%v, used $%v, left $%v", when, got, w, meter, used, left)
	}
}

// The owner's decision (#1751): a mission with nothing left does not refuse its lead. The lead's turns run under a
// $5 wrap-up allowance, are charged to the meter and counted against it, and once it is used a message is refused
// without starting the runtime, naming how to raise the budget; raising it resumes everything.
func TestMissionWrapUpAllowanceBoundsTheLeadsSpend(t *testing.T) {
	// claude reports the resumed session's running total: the turns cost 2, 2 and 3, then 1
	r := newLeadSpendRig(t, "2\n4\n7\n8\n", true)
	exhaust(t, r, 6)

	if err := r.send("t1"); err != nil {
		t.Fatalf("the first wrap-up turn: %v", err)
	}
	wantAllowance(t, r, "turn 1", 8, 2, 3)
	wantMeter(t, r, "turn 1", 8, tasks.SessionPaused) // halted as after any lead turn on a spent mission
	if first := r.execs(t)[0]; !strings.Contains(first, "--budget-usd 5 ") {
		t.Errorf("a new session's turn is not capped at the allowance: %s", first)
	}
	for _, want := range []string{"MISSION BUDGET USED UP", "$5.00 is left", "task session budget", "mission_task_finish"} {
		if p := r.systemPrompt(t); !strings.Contains(p, want) {
			t.Errorf("the lead is not told %q", want)
		}
	}

	if err := r.send("t2"); err != nil {
		t.Fatalf("the second wrap-up turn: %v", err)
	}
	wantAllowance(t, r, "turn 2", 10, 4, 1)
	// monomind compares a resumed claude session's running total with the cap (#1744): no cap is passed
	if second := r.execs(t)[1]; strings.Contains(second, "--budget-usd") || !strings.Contains(second, "--resume th_lead") {
		t.Errorf("the resumed turn: %s", second)
	}
	if p := r.systemPrompt(t); !strings.Contains(p, "$3.00 is left") {
		t.Errorf("the lead is not told what is left")
	}

	if err := r.send("t3"); err != nil {
		t.Fatalf("the third wrap-up turn: %v", err)
	}
	wantAllowance(t, r, "turn 3", 13, 7, 0)

	for _, id := range []string{"t4", "t5"} {
		err := r.send(id)
		if err == nil {
			t.Fatalf("%s ran after the allowance was used", id)
		}
		for _, want := range []string{"wrap-up allowance is used up", "task session budget 1 --usd", "task session resume 1", "--profile default", "$13.00"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not say %q: %v", want, err)
			}
		}
	}
	if n := len(r.execs(t)); n != 3 {
		t.Fatalf("the runtime was started %d times, want 3: a refused message spends nothing", n)
	}
	wantAllowance(t, r, "the refusals", 13, 7, 0)

	// raised and resumed: the mission goes on, without the wrap-up brief and without a cap from it
	more := 40.0
	if _, err := r.store.UpdateSession(context.Background(), "default", r.session, tasks.SessionUpdate{BudgetUSD: &more}, leadOperator); err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.ResumeSession(context.Background(), "default", r.session, leadOperator); err != nil {
		t.Fatal(err)
	}
	if err := r.send("t6"); err != nil {
		t.Fatalf("after the raise: %v", err)
	}
	wantMeter(t, r, "after the raise", 14, tasks.SessionRunning)
	if p := r.systemPrompt(t); strings.Contains(p, "MISSION BUDGET USED UP") {
		t.Error("the wrap-up brief outlived the raise")
	}
	if w := r.wrapUp(t); w != nil {
		t.Errorf("an allowance without exhaustion: %+v", *w)
	}
}

// The turn that takes the mission to its budget did not run under the allowance and uses none of it.
func TestMissionTurnThatSpendsTheBudgetKeepsTheAllowanceWhole(t *testing.T) {
	r := newLeadSpendRig(t, "6\n", true)
	if err := r.send("t1"); err != nil {
		t.Fatal(err)
	}
	wantAllowance(t, r, "the crossing turn", 6, 0, 5)
}

// A wrap-up turn's compaction would be a second paid call: it is not made.
func TestMissionWrapUpTurnIsNotCompacted(t *testing.T) {
	r := newLeadSpendRig(t, "1 warn\n9\n", true)
	exhaust(t, r, 5)
	if err := r.send("t1"); err != nil {
		t.Fatal(err)
	}
	if n := len(r.execs(t)); n != 1 {
		t.Fatalf("the runtime was started %d times: the wrap-up turn was compacted", n)
	}
	wantAllowance(t, r, "the turn", 6, 1, 4)
}

// A turn whose cost nobody knows counts as all that was left; a turn that never reached a model counts as nothing.
func TestMissionWrapUpUnknownCost(t *testing.T) {
	r := newLeadSpendRig(t, "nostart\nauth\nnone\n1\n", true)
	exhaust(t, r, 5)
	for _, id := range []string{"t1", "t2"} {
		if err := r.send(id); err == nil {
			t.Fatalf("%s: the broken runtime's turn did not fail", id)
		}
		wantAllowance(t, r, id, 5, 0, 5)
	}
	if err := r.send("t3"); err != nil {
		t.Fatal(err)
	}
	wantMeter(t, r, "the cost-less turn", 5, tasks.SessionPaused)
	if w := r.wrapUp(t); w == nil || w.HeldUSD != 5 || w.LeftUSD != 0 {
		t.Fatalf("a turn without a cost: %+v", w)
	}
	if err := r.send("t4"); err == nil || !strings.Contains(err.Error(), "reported no cost") {
		t.Fatalf("the turn after a cost-less one: %v", err)
	}
	if n := len(r.execs(t)); n != 3 {
		t.Fatalf("the runtime was started %d times, want 3", n)
	}
}

// The operator's pause lifts no limit: the lead's turns on a spent mission it paused run under the same allowance,
// are charged and counted against it, and are refused once it is used.
func TestMissionOperatorPauseKeepsTheWrapUpLimit(t *testing.T) {
	r := newLeadSpendRig(t, "2\n7\n", true)
	if _, err := r.store.PauseSession(context.Background(), "default", r.session, leadOperator); err != nil {
		t.Fatal(err)
	}
	exhaust(t, r, 6)
	if err := r.send("t1"); err != nil {
		t.Fatal(err)
	}
	wantAllowance(t, r, "turn 1", 8, 2, 3)
	if p := r.systemPrompt(t); !strings.Contains(p, "MISSION BUDGET USED UP") || strings.Contains(p, "mission_task_finish") {
		t.Errorf("the brief under the operator's pause: %s", p)
	}
	if err := r.send("t2"); err != nil {
		t.Fatal(err)
	}
	wantAllowance(t, r, "turn 2", 13, 7, 0)
	if err := r.send("t3"); err == nil || !strings.Contains(err.Error(), "wrap-up allowance is used up") {
		t.Fatalf("the turn after the allowance: %v", err)
	}
	if n := len(r.execs(t)); n != 2 {
		t.Fatalf("the runtime was started %d times, want 2", n)
	}
	wantMeter(t, r, "the refusal", 13, tasks.SessionPaused)
}

// The operator's pause of a mission with money left changes nothing: the chat goes on uncharged and unbriefed.
func TestMissionOperatorPauseUnspentHasNoWrapUp(t *testing.T) {
	r := newLeadSpendRig(t, "2\n4\n", true)
	if _, err := r.store.PauseSession(context.Background(), "default", r.session, leadOperator); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t1", "t2"} {
		if err := r.send(id); err != nil {
			t.Fatal(err)
		}
		if p := r.systemPrompt(t); strings.Contains(p, "MISSION BUDGET USED UP") {
			t.Error("an unspent mission got the wrap-up brief")
		}
	}
	wantMeter(t, r, "two turns", 0, tasks.SessionPaused)
}

// A turn nobody typed (a check-in, a wake-up, another agent's message) is not run on a spent mission: the allowance
// is kept whole for the user's own message.
func TestMissionWrapUpRefusesAutomaticTurns(t *testing.T) {
	r := newLeadSpendRig(t, "2\n", true)
	exhaust(t, r, 6)
	for _, id := range []string{"checkin-1", "pursuit-1-a", "master-1", "answer-d-1", "ask-r-1"} {
		err := r.send(id)
		if err == nil || !strings.Contains(err.Error(), "wrap-up") || !strings.Contains(err.Error(), "your own message") {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if n := len(r.execs(t)); n != 0 {
		t.Fatalf("the runtime was started %d times for automatic turns", n)
	}
	wantAllowance(t, r, "the automatic turns", 6, 0, 5)
	if err := r.send("t1"); err != nil {
		t.Fatalf("the user's own message: %v", err)
	}
	wantAllowance(t, r, "the user's message", 8, 2, 3)
}

// `chat history spend-cap` reports the allowance as what the next message runs under, and says when it is used up.
func TestSpendCapShowsTheWrapUpAllowance(t *testing.T) {
	r := newLeadSpendRig(t, "2\n10\n", true)
	if got := r.spendCap(t); got.Bounded || got.Source != "" {
		t.Fatalf("with money left: %+v", got)
	}
	exhaust(t, r, 5)
	want := func(when string, left float64, usedUp bool) {
		t.Helper()
		got := r.spendCap(t)
		if !got.Bounded || got.Source != spendCapWrapUp || math.Abs(got.CapUSD-left) > 1e-9 || got.TotalUSD != 5 ||
			got.UsedUp != usedUp || got.UnenforceableOn != "" || got.WorkersCapUSD != 0 {
			t.Errorf("%s: %+v", when, got)
		}
	}
	want("spent", 5, false)
	if err := r.send("t1"); err != nil {
		t.Fatal(err)
	}
	want("after a turn", 3, false)
	if err := r.send("t2"); err != nil {
		t.Fatal(err)
	}
	want("used up", 0, true)
}

// The per-message cap of a wrap-up turn is what is left, never above a lower cap the turn already has, and is left
// out where the runtime would compare it with something else (a resumed claude session) or takes none (kilo).
func TestMissionWrapUpCap(t *testing.T) {
	for _, tc := range []struct {
		runtime, resume string
		have, want      float64
	}{
		{"claude", "", 0, 3},
		{"claude", "", 10, 3},
		{"claude", "", 1, 1},
		{"claude", "s", 0, 0},
		{"claude", "s", 10, 10},
		{"codex", "s", 0, 3},
		{"codex", "", 10, 3},
		{"kilo", "", 0, 0},
	} {
		opts := monomind.ExecOptions{Runtime: tc.runtime, Resume: tc.resume, BudgetUSD: tc.have}
		capWrapUpTurn(&opts, 3)
		if opts.BudgetUSD != tc.want {
			t.Errorf("%s resume %q with cap %v: %v, want %v", tc.runtime, tc.resume, tc.have, opts.BudgetUSD, tc.want)
		}
	}
}
