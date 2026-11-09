package coderhost

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// The switch-on rules (S5b): what a wake-up turn may spend, when the loop may not act at all, and the money the lead has
// handed to helpers.

// queueWake runs the first epoch and leaves its wake-up queued (not yet run by the host).
func (w *pworld) queueWake() Command {
	w.t.Helper()
	w.firstEpoch()
	cs := w.wakes()
	if len(cs) != 1 {
		w.t.Fatalf("setup: %d wake-ups queued", len(cs))
	}
	return cs[0]
}

func (w *pworld) leadBudget(usd float64) {
	w.exec(`UPDATE coder_sessions SET budget_usd = ? WHERE profile_id = 'p' AND conversation_id = 'm'`, usd)
}

func (w *pworld) launched() *fakeLauncher { return w.h.L.(*fakeLauncher) }

func (w *pworld) commandStatus(id int64) (status, reason string) {
	w.t.Helper()
	if err := w.st.db.QueryRow(`SELECT status, COALESCE(error, '') FROM coder_session_commands WHERE id = ?`, id).Scan(&status, &reason); err != nil {
		w.t.Fatal(err)
	}
	return
}

func TestAWakeTurnRunsUnderTheLowerOfTheLeadsRemainderAndTheWakeCap(t *testing.T) {
	for name, c := range map[string]struct{ lead, want float64 }{
		"a rich lead is held to X":   {10, DefaultWakeCapUSD},
		"a poor lead keeps its own":  {0.2, 0.2},
		"a lead with exactly X left": {2, 2},
		"just above X":               {2.5, 2},
		"a lead with a cent or so":   {0.01, 0.01},
	} {
		t.Run(name, func(t *testing.T) {
			w := newPursuitWorld(t)
			w.queueWake()
			w.leadBudget(c.lead)
			if err := w.h.Tick(); err != nil {
				t.Fatal(err)
			}
			l := w.launched()
			if len(l.caps) != 1 || math.Abs(l.caps[0]-c.want) > 1e-9 {
				t.Fatalf("the wake turn started under caps %v, want [%v]", l.caps, c.want)
			}
			var rec float64
			if err := w.st.db.QueryRow(`SELECT cap_usd FROM coder_session_turn_caps WHERE profile_id = 'p' AND conversation_id = 'm' AND turn_id LIKE 'pursuit-%'`).Scan(&rec); err != nil || math.Abs(rec-c.want) > 1e-9 {
				t.Fatalf("the recorded cap is %v (%v), want %v: the meter reads the recorded cap", rec, err, c.want)
			}
		})
	}
}

func TestTheWakeCapIsTheHostsSettingNotAConstant(t *testing.T) {
	w := newPursuitWorld(t)
	w.h.Pursuit.WakeCapUSD = 0.75
	w.queueWake()
	w.leadBudget(10)
	if err := w.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if l := w.launched(); len(l.caps) != 1 || l.caps[0] != 0.75 {
		t.Fatalf("caps %v, want [0.75]", l.caps)
	}
}

func TestTheWakeCapAppliesToPursuitTurnsOnly(t *testing.T) {
	for _, turn := range []string{"person-1", "other-3"} { // a turn that costs nothing reported is charged its whole cap: one turn per world
		w := newPursuitWorld(t)
		w.leadBudget(10)
		send(t, w.st, "m", turn, "hello")
		if err := w.h.Tick(); err != nil {
			t.Fatal(err)
		}
		if got := w.launched().caps; len(got) != 1 || got[0] != 10 {
			t.Fatalf("%s: a turn that is not a pursuit wake-up ran under caps %v, want its whole 10 USD", turn, got)
		}
	}
}

func TestAWakeIsNeverStartedWithoutASpendCap(t *testing.T) {
	// the rule itself
	w := newPursuitWorld(t)
	if _, _, err := w.h.pursuitWakeCap(0, false, nil); err == nil {
		t.Fatal("an uncapped pursuit turn was allowed")
	}
	if c, capped, err := w.h.pursuitWakeCap(7, true, nil); err != nil || !capped || c != DefaultWakeCapUSD {
		t.Fatalf("pursuitWakeCap(7) = %v %v %v", c, capped, err)
	}
	// end to end: the lead lost its cap after the wake-up was queued
	cmd := w.queueWake()
	w.leadBudget(0)
	if err := w.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(w.launched().started) != 0 {
		t.Fatalf("an uncapped lead was woken: %v", w.launched().started)
	}
	if st, why := w.commandStatus(cmd.ID); st != CmdFailed {
		t.Fatalf("the wake-up is %s (%s), want failed", st, why)
	}
}

func TestAnUnboundMissionWaitsLikeAnUncappedLead(t *testing.T) { // gap G2
	w := newPursuitWorld(t)
	if got := w.h.pursuitStopReason(pursuitMission{ID: 77, Profile: "p", Conv: ""}, pursuitState{}); got != pursuitWaitingLead {
		t.Fatalf("a mission bound to no chat has stop reason %q, want %q", got, pursuitWaitingLead)
	}
}

func TestAnUncappedLeadOrAnUnknownMissionBudgetWaitsWithoutAnyCall(t *testing.T) {
	for name, set := range map[string]func(*pworld){
		"the lead has no budget":          func(w *pworld) { w.leadBudget(0) },
		"the mission budget is not known": func(w *pworld) { w.exec(`UPDATE task_sessions SET budget_usd = 0 WHERE id = 77`) },
	} {
		t.Run(name, func(t *testing.T) {
			w := newPursuitWorld(t)
			set(w)
			w.firstEpoch()
			if w.waiting() == "" || w.model.count() != 0 || len(w.wakes()) != 0 || len(w.epochs()) != 0 {
				t.Fatalf("waiting %q, judge calls %d, wake-ups %d, epochs %v", w.waiting(), w.model.count(), len(w.wakes()), w.epochs())
			}
		})
	}
}

func TestAQueuedWakeDoesNotRunOnAHostWhereTheLoopIsOff(t *testing.T) { // gap G1
	for name, off := range map[string]func(*Host){
		"no deps at all": func(h *Host) { h.Pursuit = nil },
		"no judge model": func(h *Host) {
			h.Pursuit = &PursuitDeps{Refuse: func(string) bool { return false }, WakeCapUSD: 2, LeadPin: h.Pursuit.LeadPin}
		},
		"no cap":              func(h *Host) { h.Pursuit.WakeCapUSD = 0 },
		"no way to read pins": func(h *Host) { h.Pursuit.LeadPin = nil },
	} {
		t.Run(name, func(t *testing.T) {
			w := newPursuitWorld(t)
			cmd := w.queueWake()
			off(w.h) // the host was restarted with the switch off
			if err := w.h.Tick(); err != nil {
				t.Fatal(err)
			}
			if len(w.launched().started) != 0 {
				t.Fatalf("a pursuit wake-up ran on a host where the loop is off: %v", w.launched().started)
			}
			if st, why := w.commandStatus(cmd.ID); st != CmdFailed || why == "" {
				t.Fatalf("the wake-up is %s (%q), want failed with a reason", st, why)
			}
		})
	}
}

func TestAPursuitSendFromAnywhereFailsOnAHostWhereTheLoopIsOff(t *testing.T) {
	h, l, st := newHost(t) // no Pursuit at all
	send(t, st, "c1", PursuitTurnPrefix+"9-aa", "[PURSUIT] x")
	if err := h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(l.started) != 0 {
		t.Fatalf("a pursuit- turn started on a host without the loop: %v", l.started)
	}
}

func TestPauseLeavesWorkInFlightAndQueuesNothingNew(t *testing.T) {
	w := newPursuitWorld(t)
	w.queueWake()
	if err := w.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(w.launched().started) != 1 {
		t.Fatal("setup: the wake turn did not start")
	}
	w.pause()
	before, calls, epochs := len(w.launched().started), w.model.count(), len(w.epochs())
	for i := 0; i < 3; i++ {
		w.advance(2 * time.Hour)
		w.pass()
		if err := w.h.Tick(); err != nil {
			t.Fatal(err)
		}
	}
	for _, tn := range w.launched().turns {
		if tn.stopped {
			t.Fatal("pausing the pursuit stopped a turn that was already running")
		}
	}
	if len(w.launched().started) != before || w.model.count() != calls || len(w.epochs()) != epochs || w.wakeStatuses()["pending"] > 0 {
		t.Fatalf("a paused pursuit started %d turn(s), %d judge call(s), %d epoch(s) more; pending wake-ups %v", len(w.launched().started)-before, w.model.count()-calls, len(w.epochs())-epochs, w.wakeStatuses())
	}
}

func (w *pworld) wakeStatuses() map[string]int {
	w.t.Helper()
	rows, err := w.st.db.Query(`SELECT status, COUNT(*) FROM coder_session_commands WHERE kind = 'send' AND turn_id LIKE 'pursuit-%' GROUP BY status`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		_ = rows.Scan(&s, &n)
		out[s] = n
	}
	return out
}

func TestAPausedPursuitDoesNotRunAWakeQueuedBeforeThePause(t *testing.T) {
	w := newPursuitWorld(t)
	cmd := w.queueWake()
	w.pause()
	if err := w.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if len(w.launched().started) != 0 {
		t.Fatal("a paused pursuit's queued wake-up ran")
	}
	if st, _ := w.commandStatus(cmd.ID); st != CmdFailed {
		t.Fatalf("the wake-up is %s", st)
	}
}

// addHelper puts a child chat under the lead with a carved budget.
func (w *pworld) addHelper(id string, carved float64) {
	w.t.Helper()
	addConversation(w.t, w.st, id, "/work/m")
	if _, err := w.st.EnsureSession("p", id); err != nil {
		w.t.Fatal(err)
	}
	w.exec(`UPDATE coder_sessions SET parent_conversation_id = 'm', budget_usd = ? WHERE profile_id = 'p' AND conversation_id = ?`, carved, id)
}

func TestMoneyCarvedToHelpersAndNotSpentCountsInTheTwentyPercent(t *testing.T) {
	// the mission budget is 10 and the limit 20%: 2 USD
	small := newPursuitWorld(t)
	small.leadBudget(20)
	small.addHelper("h1", 0.5)
	small.firstEpoch()
	if small.waiting() != "" || len(small.epochs()) != 1 {
		t.Fatalf("a small carve stopped the pursuit: waiting %q epochs %v", small.waiting(), small.epochs())
	}
	big := newPursuitWorld(t)
	big.leadBudget(20)
	big.addHelper("h1", 1.5)
	big.addHelper("h2", 1.5)
	big.firstEpoch()
	if big.waiting() != pursuitWaitingSpend || big.model.count() != 0 || len(big.wakes()) != 0 {
		t.Fatalf("3 USD carved and unspent against a 2 USD limit: waiting %q, judge calls %d, wake-ups %d", big.waiting(), big.model.count(), len(big.wakes()))
	}
	// a carve that was returned (the child ended: its budget is lowered to what it spent) no longer counts
	big.exec(`UPDATE coder_sessions SET budget_usd = 0.0000001 WHERE profile_id = 'p' AND parent_conversation_id = 'm'`)
	big.advance(31 * time.Minute)
	big.pass()
	if big.waiting() != "" {
		t.Fatalf("returned carves still count: waiting %q", big.waiting())
	}
}

func TestOnlyHelpersCarvesAreUnspentCarves(t *testing.T) {
	w := newPursuitWorld(t)
	w.leadBudget(20)
	w.addHelper("h1", 1.5)
	micro, err := w.st.helperCarveUnspentMicro(pursuitMission{ID: 77, Profile: "p", Conv: "m"}, []string{"m", "h1"})
	if err != nil || micro != 1_500_000 {
		t.Fatalf("unspent carve = %d (%v), want 1500000", micro, err)
	}
	// the lead's own budget is not a carve
	micro, _ = w.st.helperCarveUnspentMicro(pursuitMission{ID: 77, Profile: "p", Conv: "m"}, []string{"m"})
	if micro != 0 {
		t.Fatalf("the lead's own budget counted as a carve: %d", micro)
	}
}

func TestAJudgeThatEqualsTheLeadsPinMakesThePursuitWait(t *testing.T) {
	for name, lead := range map[string]string{
		"the same runtime and model":      "fake/judge",
		"the same model, another version": "fake/judge@9",
		"a lead that is not pinned":       "",
	} {
		t.Run(name, func(t *testing.T) {
			w := newPursuitWorld(t)
			w.leadPin = lead
			w.firstEpoch()
			if w.waiting() != pursuitWaitingPin || w.model.count() != 0 || len(w.wakes()) != 0 || len(w.epochs()) != 0 {
				t.Fatalf("waiting %q, judge calls %d, wake-ups %d, epochs %v", w.waiting(), w.model.count(), len(w.wakes()), w.epochs())
			}
			if why := missionpursuit.WaitingWhy(w.waiting()); !strings.Contains(why, "model") {
				t.Fatalf("the reason is not plain: %q", why)
			}
			// once the lead runs another model the pursuit goes on
			w.leadPin = "claude/lead-model"
			w.advance(31 * time.Minute)
			w.pass()
			w.advance(31 * time.Minute)
			w.pass()
			if w.waiting() != "" || w.model.count() == 0 {
				t.Fatalf("after the lead changed model: waiting %q, judge calls %d", w.waiting(), w.model.count())
			}
		})
	}
}

func TestTheLeadsPinIsReadAtCallTime(t *testing.T) {
	w := newPursuitWorld(t)
	refuse := w.h.pursuitRefuse(pursuitMission{ID: 77, Profile: "p", Conv: "m"})
	if refuse("fake/judge@1") {
		t.Fatal("a judge on another model than the lead was refused")
	}
	w.leadPin = "fake/judge" // the lead's model is changed to the judge's after the engine was built
	if !refuse("fake/judge@1") {
		t.Fatal("a judge that now equals the lead's pin was not refused at the call")
	}
	if !refuse("") {
		t.Fatal("an empty pin judged")
	}
	w.leadPin = ""
	if !refuse("fake/judge@1") {
		t.Fatal("a lead of unknown model was taken as independent")
	}
}

func TestThePursuitIsNotReadyWithoutAValidWakeCapAndLeadPin(t *testing.T) {
	ok := func() *PursuitDeps {
		return &PursuitDeps{Model: &fakeJudge{}, Refuse: func(string) bool { return false }, WakeCapUSD: 2, LeadPin: func(string, string) string { return "a/b" }}
	}
	if !ok().ready() {
		t.Fatal("a complete PursuitDeps is not ready")
	}
	for name, mut := range map[string]func(*PursuitDeps){
		"zero cap":     func(d *PursuitDeps) { d.WakeCapUSD = 0 },
		"negative cap": func(d *PursuitDeps) { d.WakeCapUSD = -1 },
		"NaN cap":      func(d *PursuitDeps) { d.WakeCapUSD = math.NaN() },
		"infinite cap": func(d *PursuitDeps) { d.WakeCapUSD = math.Inf(1) },
		"above max":    func(d *PursuitDeps) { d.WakeCapUSD = MaxWakeCapUSD + 0.01 },
		"no lead pin":  func(d *PursuitDeps) { d.LeadPin = nil },
	} {
		d := ok()
		mut(d)
		if d.ready() {
			t.Errorf("%s: ready", name)
		}
	}
	if d := ok(); func() bool { d.WakeCapUSD = MaxWakeCapUSD; return !d.ready() }() {
		t.Error("the maximum itself is refused")
	}
}

// The scripted acceptance run with the fake judge, from the start to "waiting for you": every wake turn is capped at X, the
// away limit of wake-ups stops it, the person's action starts it again.
func TestAScriptedPursuitFromStartToWaiting(t *testing.T) {
	w := newPursuitWorldLimits(t, missionpursuit.AwayLimits{Wakes: 3})
	w.leadBudget(20)
	w.exec(`UPDATE task_sessions SET budget_usd = 100 WHERE id = 77`) // the first wake turn is charged its whole cap: 20% of 10 USD would stop after one
	w.h.Pursuit.MaxEpochsPerHour = 12
	w.pass()
	for i := 0; i < 2; i++ {
		w.advance(31 * time.Minute)
		w.pass()
		if err := w.h.Tick(); err != nil { // the lead takes the wake-up
			t.Fatal(err)
		}
		if w.launched().turn(w.lastWakeTurn()) == nil {
			t.Fatalf("round %d: the wake-up did not start; commands %v, waiting %q, epochs %v", i, w.wakeStatuses(), w.waiting(), w.epochs())
		}
		w.launched().turn(w.lastWakeTurn()).end()
		w.setLead("idle", "")
		w.endCommandsDone()
	}
	if got := w.launched().caps; len(got) != 2 || got[0] != DefaultWakeCapUSD || got[1] != DefaultWakeCapUSD {
		t.Fatalf("the two wake turns ran under caps %v, want X twice", got)
	}
	// The third wake-up reaches the limit of 3 when it is queued, so the run-time check refuses it (it counts itself): the
	// pursuit waits for the person.
	w.advance(31 * time.Minute)
	w.pass()
	if err := w.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if w.waiting() != "wakes" || w.model.count() != 3 || len(w.launched().started) != 2 {
		t.Fatalf("waiting %q, judge calls %d, turns started %d", w.waiting(), w.model.count(), len(w.launched().started))
	}
	w.advance(10 * time.Hour)
	w.pass()
	if w.model.count() != 3 {
		t.Fatal("a pursuit waiting for the person went on")
	}
}

func (w *pworld) lastWakeTurn() string {
	w.t.Helper()
	var turn string
	if err := w.st.db.QueryRow(`SELECT turn_id FROM coder_session_commands WHERE kind = 'send' AND turn_id LIKE 'pursuit-%' ORDER BY id DESC LIMIT 1`).Scan(&turn); err != nil {
		w.t.Fatal(err)
	}
	return turn
}
