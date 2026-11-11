package coderhost

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// Revert proof: take away.Spend(checks.Micro) out of the gate and the checks below are free to the 20% rule.
func TestChecksAloneCanCrossTheTwentyPercentRule(t *testing.T) {
	w := newCW(t)
	w.pass()                         // the loop records its state: the base of the counts
	for i := int64(1); i <= 5; i++ { // 5 x $0.50 of unknown cost against 20% of a $10 budget
		w.rawCheck(i, "blocked", "reviewer-unavailable", 0, 0)
	}
	w.pass()
	if got := w.waiting(); got != pursuitWaitingSpend {
		t.Fatalf("waiting = %q: five checks at the cap are $2.50 of a $2 share", got)
	}
}

// What a check cost, as the loop counts it: a failed machine check nothing, a reviewer call at its reported cost, one that
// reported none at the cap. Revert proof: take CostKnown out of CheckSpendAfter's rule, or the library's known zero.
func TestWhatAChecksCostsIsCountedAsTheRowSays(t *testing.T) {
	for _, c := range []struct {
		name   string
		o      verifier.Outcome
		reason verifier.Reason
		micro  int64
		known  bool
		want   int64
	}{
		{"a failed machine check", verifier.OutcomeFailed, verifier.ReasonCriteriaFailed, 0, true, 0},
		{"a reviewer cost reported", verifier.OutcomeBlocked, verifier.ReasonReviewerBlocked, 123456, true, 123456},
		{"a reviewer cost not reported", verifier.OutcomeBlocked, verifier.ReasonReviewerUnavailable, 0, false, missionpursuit.CheckCapMicro},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newCW(t)
			w.fn = func(_ context.Context, req verifier.Request) (verifier.Result, error) {
				return resultOf(req, c.o, c.reason, c.micro, c.known), nil
			}
			w.untilChecked(1)
			if s := w.spend(); s.Micro != c.want || s.Count != 1 {
				t.Errorf("spend = %+v, want %d", s, c.want)
			}
		})
	}
}

func TestAPassedCheckWaitsForThePersonAndNothingSpendsMeanwhile(t *testing.T) {
	w := newCW(t)
	w.fn = func(_ context.Context, req verifier.Request) (verifier.Result, error) {
		return resultOf(req, verifier.OutcomePassedMachine, "", 0, false), nil
	}
	w.untilChecked(1)
	if got := w.checks(); len(got) != 1 || got[0] != "1:done:passed-machine:" {
		t.Fatalf("checks = %v", got)
	}
	if s := w.spend(); s.Micro != missionpursuit.CheckCapMicro {
		t.Errorf("an unreported reviewer cost counts at the cap: %d", s.Micro)
	}
	w.exec(`UPDATE coder_sessions SET unattended = 1 WHERE conversation_id = 'm'`)
	judges, wakes := w.model.count(), w.wakeRows()
	for i := 0; i < 6; i++ {
		w.pass()
		w.wait()
		w.advance(31 * time.Minute)
	}
	if got := w.waiting(); got != pursuitWaitingAnswer {
		t.Errorf("waiting = %q, want %q", got, pursuitWaitingAnswer)
	}
	if w.model.count() != judges || w.wakeRows() != wakes || w.checkRows() != 1 {
		t.Errorf("spent while the answer is the person's: judge %d->%d wakes %d->%d checks %d", judges, w.model.count(), wakes, w.wakeRows(), w.checkRows())
	}
}

// An outcome that is not a pass is never a pass: a criterion that ended in error ends the check in error.
func TestACriterionInErrorNeverBecomesAPass(t *testing.T) {
	w := newCW(t)
	w.fn = func(_ context.Context, req verifier.Request) (verifier.Result, error) {
		res := resultOf(req, verifier.OutcomeError, verifier.ReasonCriterionError, 0, true)
		res.Criteria[0].Result, res.Criteria[0].Detail = verifier.ResError, "the confinement failed, not the criterion: unit failed"
		return res, nil
	}
	w.untilChecked(1)
	if got := w.checks(); len(got) != 1 || got[0] != "1:done:error:criterion-error" {
		t.Fatalf("checks = %v", got)
	}
	if got := w.waiting(); got == pursuitWaitingAnswer {
		t.Error("an errored check waits for the person's answer as if it had passed")
	}
}

func TestTheBaseOfTheCheckCountMovesWhenThePersonActs(t *testing.T) {
	w := newCW(t)
	w.pass()
	w.rawCheck(1, "failed", "criteria-failed", 0, 1)
	w.rawCheck(2, "failed", "criteria-failed", 0, 1)
	w.personActs()
	st, _, err := w.st.pursuitStateOf(77)
	if err != nil || st.BaseCheckID != 2 {
		t.Fatalf("base_check_id = %d %v, want the head of the check ledger (2)", st.BaseCheckID, err)
	}
	if s := w.spend(); s.Count != 2 {
		t.Errorf("the ledger itself is not touched: %+v", s)
	}
}

func TestTheMeterRefusesWhatTheLoopWouldNotAllow(t *testing.T) {
	w := newCW(t)
	w.pass()
	m := checkMeter{w.h}
	if _, err := m.Reserve(context.Background(), 0.5); err == nil {
		t.Error("a call that names no mission was reserved")
	}
	ctx := withCheckSession(context.Background(), 77)
	if _, err := m.Reserve(ctx, 0.5); err != nil {
		t.Errorf("a plain call was refused: %v", err)
	}
	if _, err := m.Reserve(ctx, 5); err == nil {
		t.Error("a call above the cap was reserved")
	}
	for i := int64(1); i <= missionpursuit.MaxHostChecksPerHour+1; i++ {
		w.rawCheck(i, "failed", "criteria-failed", 0, 1)
	}
	if _, err := m.Reserve(ctx, 0.5); err == nil {
		t.Error("a call was reserved with the host's checks of the hour used")
	}
	w.advance(61 * time.Minute)
	w.pause()
	if _, err := m.Reserve(ctx, 0.5); err == nil {
		t.Error("a call was reserved for a paused pursuit")
	}
}

// The relation of the switches: the checks need the loop; a checker that equals the judge or the lead is a waiting reason; a
// checker that cannot be built or a folder that cannot be used is another, and none of them starts a check.
func TestTheChecksNeedTheLoopAndIndependentPinsAndAFolder(t *testing.T) {
	t.Run("the loop is off", func(t *testing.T) {
		w := newCW(t)
		w.h.Pursuit.Model = nil
		for i := 0; i < 10; i++ {
			w.step()
		}
		if w.checkRows() != 0 || w.model.count() != 0 {
			t.Fatalf("the verifier ran with the loop off: %v", w.checks())
		}
		if w.h.Pursuit.verifierReady() {
			t.Error("verifierReady with no judge")
		}
	})
	t.Run("the checker is the judge", func(t *testing.T) {
		w := newCW(t)
		w.dep.Pin = "fake/judge@2"
		w.step()
		if got := w.waiting(); got != pursuitWaitingVerifierPin {
			t.Fatalf("waiting = %q", got)
		}
	})
	t.Run("the checker is the lead", func(t *testing.T) {
		w := newCW(t)
		w.dep.Pin = "Claude/Lead-Model@9"
		w.step()
		if got := w.waiting(); got != pursuitWaitingVerifierPin {
			t.Fatalf("waiting = %q", got)
		}
	})
	t.Run("the lead's model is not known", func(t *testing.T) {
		w := newCW(t)
		w.leadPin = ""
		w.step()
		if got := w.waiting(); got == "" || w.checkRows() != 0 {
			t.Fatalf("waiting = %q checks %v", got, w.checks())
		}
	})
	t.Run("the checker cannot be built", func(t *testing.T) {
		w := newCW(t)
		w.dep.New = func(verifier.Meter) (verifier.Verifier, error) { return nil, errors.New("no scratch") }
		w.step()
		if got := w.waiting(); got != pursuitWaitingCannotCheck || w.checkRows() != 0 {
			t.Fatalf("waiting = %q checks %v", got, w.checks())
		}
	})
	t.Run("the folder is forbidden", func(t *testing.T) {
		w := newCW(t)
		w.dep.Forbidden = []string{w.root}
		w.step()
		if got := w.waiting(); got != pursuitWaitingCannotCheck {
			t.Fatalf("waiting = %q", got)
		}
		_, why := w.h.checkSubject(pursuitMission{ID: 77, Profile: "p", Conv: "m"})
		if !strings.HasPrefix(why, "bad-subject") {
			t.Errorf("why = %q", why)
		}
	})
}

func TestTheProjectFolderRules(t *testing.T) {
	w := newCW(t)
	m := pursuitMission{ID: 77, Profile: "p", Conv: "m"}
	for name, c := range map[string]struct {
		cwd       string
		forbidden []string
		ok        bool
	}{
		"a plain folder":                    {w.root, []string{"/nonexistent"}, true},
		"the root of the computer":          {"/", nil, false},
		"the home folder itself":            {w.root, []string{w.root}, false},
		"a parent of the state folder":      {w.root, []string{w.root + "/sub"}, false},
		"inside the state folder":           {w.root + "/sub", []string{w.root}, false},
		"a folder that does not exist":      {w.root + "/nope", nil, false},
		"a file":                            {w.root + "/a.txt", nil, false},
		"an empty path":                     {"", nil, false},
		"a relative path":                   {"relative/dir", nil, false},
		"a parent of the home folder":       {"/home", []string{"/home/someone"}, false},
		"a sibling of the forbidden folder": {w.root, []string{w.root + "-other"}, true},
	} {
		w.exec(`UPDATE ai_chat_conversations SET cwd = ? WHERE id = 'm'`, c.cwd)
		w.dep.Forbidden = c.forbidden
		root, why := w.h.checkSubject(m)
		if (why == "") != c.ok {
			t.Errorf("%s: root %q why %q, want ok=%v", name, root, why, c.ok)
		}
	}
}

// The judge and the checker use one rule for "the same model": the host's copy and the library's, on the same vectors.
func TestThePinRuleIsTheLibrarysRule(t *testing.T) {
	for _, p := range []string{"a/b", "A/B@1", " a/b@2 ", "a/b@", "", "  ", "x", "Claude/Opus-4@x@y"} {
		if pinKey(p) != verifier.PinKey(p) {
			t.Errorf("pinKey(%q) = %q, verifier.PinKey = %q", p, pinKey(p), verifier.PinKey(p))
		}
	}
}

// Results that cannot be recorded are no basis for an outcome: the check ends abandoned (at the cap), never as the pass it claims.
func TestAResultTheStoreRefusesIsNotAnOutcome(t *testing.T) {
	w := newCW(t)
	w.fn = func(_ context.Context, req verifier.Request) (verifier.Result, error) {
		res := resultOf(req, verifier.OutcomePassedMachine, "", 0, true)
		res.Criteria[0].Hash = strings.Repeat("a", 64) // not the locked criterion's hash: the database refuses it
		return res, nil
	}
	w.untilChecked(1)
	if got := w.checks(); len(got) != 1 || got[0][:11] != "1:abandoned" {
		t.Fatalf("checks = %v, want one abandoned check", got)
	}
	if s := w.spend(); s.Micro != missionpursuit.CheckCapMicro {
		t.Errorf("spend %d, want the cap", s.Micro)
	}
	if got := w.waiting(); got == pursuitWaitingAnswer {
		t.Error("a check whose results were refused waits for the person's answer")
	}
}
