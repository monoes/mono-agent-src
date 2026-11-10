package missionpursuit

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

func TestBeginNeedsACleanAbsoluteProjectFolder(t *testing.T) {
	w := newCheckWorld(t)
	for _, root := range []string{"", "project", "./project", "/work/../etc", "/work//project", "/work/project/", strings.Repeat("/a", 2100)} {
		if _, err := w.hc.Begin(bg, "default", w.session, BeginCheck{GoalHash: w.goal.Hash, Round: 1, SubjectRoot: root}); err == nil {
			t.Errorf("project folder %q was accepted", root)
		}
	}
	if n := count(t, w.db, `SELECT COUNT(*) FROM mission_goal_checks`); n != 0 {
		t.Errorf("%d checks after refusals", n)
	}
}

func TestARoundOnlyGrows(t *testing.T) {
	w := newCheckWorld(t)
	id, err := w.hc.Begin(bg, "default", w.session, BeginCheck{GoalHash: w.goal.Hash, Round: 4, SubjectRoot: "/work/project"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.hc.Finish(bg, id, w.result(verifier.OutcomeFailed, "")); err != nil {
		t.Fatal(err)
	}
	for _, r := range []int64{4, 3, 0} {
		if _, err := w.hc.Begin(bg, "default", w.session, BeginCheck{GoalHash: w.goal.Hash, Round: r, SubjectRoot: "/work/project"}); err == nil {
			t.Errorf("round %d was reused or went backwards", r)
		}
	}
	if _, err := w.hc.Begin(bg, "default", w.session, BeginCheck{GoalHash: w.goal.Hash, Round: 5, SubjectRoot: "/work/project"}); err != nil {
		t.Errorf("round 5: %v", err)
	}
}

func TestTheRoundIsPartOfWhatYouRead(t *testing.T) {
	a := dossierHash(1, 1, "g", "r", "ok", "s")
	if a == dossierHash(1, 2, "g", "r", "ok", "s") {
		t.Error("the dossier does not change with the round")
	}
	if a != dossierHash(1, 1, "g", "r", "ok", "s") {
		t.Error("the dossier is not deterministic")
	}
}

// Eight racers start a check: one wins and the rest get the typed error, never a raw database constraint.
func TestConcurrentBeginHasOneWinnerAndTypedLosers(t *testing.T) {
	w := newCheckWorld(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = w.hc.Begin(bg, "default", w.session, BeginCheck{GoalHash: w.goal.Hash, Round: 1, SubjectRoot: "/work/project"})
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrCheckRunning):
		default:
			t.Errorf("a loser got %v", err)
		}
	}
	if won != 1 || count(t, w.db, `SELECT COUNT(*) FROM mission_goal_checks`) != 1 {
		t.Errorf("%d winners", won)
	}
}

// Eight racers give the same answer on the same read: exactly one is recorded.
func TestConcurrentAnswersOfOneCriterionHaveOneWinner(t *testing.T) {
	w := newCheckWorld(t)
	c := w.read(t, w.passed(t))
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = w.answer(t, c, "c3", i%2 == 0, human)
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		}
	}
	if won != 1 || count(t, w.db, `SELECT COUNT(*) FROM mission_goal_answers WHERE pos = 3`) != 1 {
		t.Errorf("%d winners of one criterion: %v", won, errs)
	}
}

func TestAnAbandonedCheckCountsAtTheCapUnlessItsCostIsKnown(t *testing.T) {
	w := newCheckWorld(t)
	id := w.begin(t)
	if err := w.hc.Finish(bg, id, w.result(verifier.OutcomeAbandoned, "")); err != nil { // result() says cost 1500 known
		t.Fatal(err)
	}
	if c := w.read(t, id); c.CostMicro != 1500 || !c.CostKnown {
		t.Errorf("a known cost was replaced: %d %v", c.CostMicro, c.CostKnown)
	}
	id2 := w.begin(t)
	res := w.result(verifier.OutcomeAbandoned, "")
	res.CostMicro, res.CostKnown = 0, false
	if err := w.hc.Finish(bg, id2, res); err != nil {
		t.Fatal(err)
	}
	if c := w.read(t, id2); c.CostMicro != CheckCapMicro || c.CostKnown {
		t.Errorf("an abandoned check with no cost: %d known %v, want the cap %d unknown", c.CostMicro, c.CostKnown, CheckCapMicro)
	}
	id3 := w.begin(t)
	if n, err := w.hc.AbandonRunning(bg); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if c := w.read(t, id3); c.CostMicro != CheckCapMicro || c.CostKnown {
		t.Errorf("a swept check: %d known %v", c.CostMicro, c.CostKnown)
	}
	id4 := w.begin(t)
	res = w.result(verifier.OutcomeFailed, "")
	res.CostMicro = 1 << 40
	if err := w.hc.Finish(bg, id4, res); err == nil {
		t.Error("an absurd cost was recorded")
	}
}

func TestAnswerableSaysWhyNot(t *testing.T) {
	w := newCheckWorld(t)
	failed := w.begin(t)
	if err := w.hc.Finish(bg, failed, w.result(verifier.OutcomeFailed, "")); err != nil {
		t.Fatal(err)
	}
	if err := w.store.Answerable(bg, "default", w.session, failed); err == nil || !strings.Contains(err.Error(), "did not pass") {
		t.Errorf("a failed check: %v", err)
	}
	ok := w.passed(t)
	if err := w.store.Answerable(bg, "default", w.session, ok); err != nil {
		t.Errorf("the latest passed check: %v", err)
	}
	if err := w.store.Answerable(bg, "default", w.session, failed); err == nil {
		t.Error("an older check")
	}
	if err := w.store.Answerable(bg, "default", w.session, 999); !errors.Is(err, ErrNoCheck) {
		t.Errorf("no such check: %v", err)
	}
}

func TestCleanDoesWhatItSays(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		max  int
		want string
	}{
		"escape":               {"a\x1b[31mred", 100, "a [31mred"},
		"osc with bell":        {"x\x1b]0;PWNED\x07y", 100, "x ]0;PWNED y"},
		"bidi and zero width":  {"a‮b​c", 100, "a b c"},
		"newline and tab":      {"l1\nl2\tl3", 100, "l1 l2 l3"},
		"line separators":      {"a b c­d", 100, "a b c d"},
		"invalid run is one ?": {"a\xff\xfe\xfdb", 100, "a?b"},
		"private use stays":    {"ab", 100, "ab"},
		"unassigned stays":     {"a\U0010fffeb", 100, "a\U0010fffeb"},
		"cut on a rune":        {strings.Repeat("日", 10), 7, "日日"},
		"short":                {"ok", 100, "ok"},
		"empty":                {"", 100, ""},
	} {
		if got := Clean(c.in, c.max); got != c.want {
			t.Errorf("%s: Clean(%q, %d) = %q, want %q", name, c.in, c.max, got, c.want)
		}
	}
}
