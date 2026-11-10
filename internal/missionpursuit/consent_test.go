package missionpursuit

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func okFacts() LiveFacts {
	return LiveFacts{Cost: "table", HostOn: true, JudgePin: "claude/haiku@4", WakeCapUSD: 2, BudgetKnown: true, LeadCapped: true}
}

func liveOf(f LiveFacts) LiveFunc {
	return func(context.Context, tasks.Queryer, string, AwayLimits) (LiveFacts, error) { return f, nil }
}

// beginReq is a begin for exactly what a preview with the facts f shows.
func beginReq(t *testing.T, fx fixture, f LiveFacts) BeginRequest {
	t.Helper()
	v, err := fx.store.Preview(bg, "default", fx.session, AwayLimits{}, liveOf(f))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return BeginRequest{GoalHash: v.Goal.Hash, Consent: v.Consent.Digest, Chat: v.Chat, Live: liveOf(f)}
}

func snapshot(t *testing.T, fx fixture) [4]int {
	t.Helper()
	return [4]int{
		count(t, fx.db, `SELECT COUNT(*) FROM mission_pursuit`),
		count(t, fx.db, `SELECT COUNT(*) FROM mission_goals WHERE state = 'proposed'`),
		count(t, fx.db, `SELECT COUNT(*) FROM mission_goals WHERE state = 'locked'`),
		count(t, fx.db, `SELECT COUNT(*) FROM task_session_events`),
	}
}

func TestConsentDigestCoversEveryFactAndNoClock(t *testing.T) {
	g := Goal{Hash: strings.Repeat("a", 64), Criteria: []StoredCriterion{{Criterion: Criterion{Kind: KindRubric, Spec: "x"}}}}
	lim := AwayLimits{ParkedMax: 3, Pct: 20, Wakes: 6}
	base := ConsentOf(7, "chat1", g, lim, okFacts())
	if again := ConsentOf(7, "chat1", g, lim, okFacts()); again.Digest != base.Digest || len(base.Blockers) != 0 {
		t.Fatalf("not pure or blocked: %+v %+v", base, again)
	}
	mut := map[string]Consent{
		"session": ConsentOf(8, "chat1", g, lim, okFacts()),
		"chat":    ConsentOf(7, "chat2", g, lim, okFacts()),
		"goal":    ConsentOf(7, "chat1", Goal{Hash: strings.Repeat("b", 64), Criteria: g.Criteria}, lim, okFacts()),
		"pct":     ConsentOf(7, "chat1", g, AwayLimits{ParkedMax: 3, Pct: 21, Wakes: 6}, okFacts()),
		"parked":  ConsentOf(7, "chat1", g, AwayLimits{ParkedMax: 4, Pct: 20, Wakes: 6}, okFacts()),
		"wakes":   ConsentOf(7, "chat1", g, AwayLimits{ParkedMax: 3, Pct: 20, Wakes: 7}, okFacts()),
	}
	for name, mod := range map[string]func(*LiveFacts){
		"cost": func(f *LiveFacts) { f.Cost = "other" }, "pin": func(f *LiveFacts) { f.JudgePin = "x/y" },
		"cap": func(f *LiveFacts) { f.WakeCapUSD = 3 }, "checker": func(f *LiveFacts) { f.CheckerSetUp = true },
		"host": func(f *LiveFacts) { f.HostOn = false }, "invalid": func(f *LiveFacts) { f.HostInvalid = true },
	} {
		f := okFacts()
		mod(&f)
		mut[name] = ConsentOf(7, "chat1", g, lim, f)
	}
	for name, c := range mut {
		if c.Digest == base.Digest {
			t.Errorf("changing %s leaves the digest the same", name)
		}
	}
}

func TestConsentBlockers(t *testing.T) {
	g := Goal{Hash: "h", Criteria: []StoredCriterion{{Criterion: Criterion{Kind: "file", Spec: "x"}}}}
	f := LiveFacts{PinWhy: "same", HostInvalid: true}
	c := ConsentOf(1, "", g, AwayLimits{}, f)
	want := []string{BlockBudgetUnknown, BlockHostInfoInvalid, BlockHostOff, BlockJudgePin, BlockLeadUncapped, BlockNoChat, BlockNoRubric}
	if strings.Join(c.Blockers, ",") != strings.Join(want, ",") {
		t.Errorf("blockers = %v, want %v", c.Blockers, want)
	}
	for _, code := range want {
		if BlockReasons[code] == "" {
			t.Errorf("no reason for %s", code)
		}
	}
}

func TestPhraseNamesMissionHashAndEveryLimit(t *testing.T) {
	lim := AwayLimits{ParkedMax: 4, Pct: 35, Wakes: 9}
	got := Phrase(12, strings.Repeat("ab", 32), lim)
	if got != "I understand: pursue mission 12, goal abababab, stop at 35 percent spent, 4 waiting questions or 9 wake-ups" {
		t.Errorf("phrase = %q", got)
	}
	for name, other := range map[string]AwayLimits{"pct": {ParkedMax: 4, Pct: 36, Wakes: 9}, "parked": {ParkedMax: 5, Pct: 35, Wakes: 9}, "wakes": {ParkedMax: 4, Pct: 35, Wakes: 10}} {
		if Phrase(12, "ab", lim) == Phrase(12, "ab", other) {
			t.Errorf("the %s limit is not in the phrase", name)
		}
	}
}

func TestBudgetKnownIsTheLoopsPredicate(t *testing.T) {
	inf, nan := math.Inf(1), math.NaN()
	for _, c := range []struct {
		b, s float64
		want bool
	}{{20, 0, true}, {20, 5, true}, {0, 0, false}, {-1, 0, false}, {inf, 0, false}, {nan, 0, false}, {20, -1, false}, {20, inf, false}, {20, nan, false}} {
		if got := BudgetKnown(c.b, c.s); got != c.want {
			t.Errorf("BudgetKnown(%v, %v) = %v", c.b, c.s, got)
		}
	}
}

func TestEscapeHiddenAndCheckDisplayable(t *testing.T) {
	if got := EscapeHidden("a\u202eb\u200bc\U000E0041"); got != `a\u202Eb\u200Bc\u{E0041}` {
		t.Errorf("escaped = %q", got)
	}
	crit := func(spec string) []StoredCriterion {
		return []StoredCriterion{{Criterion: Criterion{Kind: "rubric", Spec: spec}}}
	}
	if err := CheckDisplayable(append(crit("plain text\nsecond line"), StoredCriterion{Criterion: Criterion{Kind: "rubric", Spec: "x", Expected: "\u00e9"}})); err != nil {
		t.Errorf("plain text refused: %v", err)
	}
	for name, spec := range map[string]string{"bidi": "a\u202eb", "zero width": "a\u200bb", "tag": "a\U000E0041", "nul": "a\x00b",
		"braille blank": "a\u2800b", "hangul filler": "a\u3164b", "choseong filler": "a\u115fb", "halfwidth filler": "a\uffa0b", "grapheme joiner": "a\u034fb",
		"emoji variation selector": "done \u2714\ufe0f", "joiner": "a\u200db"} {
		if err := CheckDisplayable(crit(spec)); !errors.Is(err, ErrHiddenText) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := CheckDisplayable(crit(strings.Repeat("x", MaxDisplayRunes+1))); !errors.Is(err, ErrCriterionTooLong) || errors.Is(err, ErrHiddenText) {
		t.Errorf("long: %v", err)
	}
	if err := CheckDisplayable(crit(strings.Repeat("x", MaxDisplayRunes))); err != nil {
		t.Errorf("a criterion of exactly the cap: %v", err)
	}
}

func TestConfirmAndStartFromAProposal(t *testing.T) {
	f := newMission(t)
	mustPropose(t, f, goodCriteria(), lead)
	req := beginReq(t, f, okFacts())
	pu, g, err := f.store.ConfirmAndStart(bg, "default", f.session, req, human)
	if err != nil || !pu.Enabled || g.State != StateLocked || g.ConfirmedBy != "you" || pu.LockedBy != "you" {
		t.Fatalf("begin: %+v %+v %v", pu, g, err)
	}
	if n := count(t, f.db, `SELECT COUNT(*) FROM task_session_events WHERE session_id = ? AND kind = 'note' AND note LIKE '%via=terminal'`, f.session); n != 1 {
		t.Errorf("begin event rows = %d", n)
	}
	_, _, err = f.store.ConfirmAndStart(bg, "default", f.session, req, human)
	wantIs(t, err, ErrAlreadyStarted, "second begin")
}

// A goal the person confirmed in the terminal first is not confirmed again: the begin accepts it for the same hash.
func TestConfirmAndStartAcceptsATerminalConfirmedGoal(t *testing.T) {
	f := confirmedMission(t)
	g0, _ := f.store.Get(bg, "default", f.session)
	req := beginReq(t, f, okFacts())
	_, g, err := f.store.ConfirmAndStart(bg, "default", f.session, req, human)
	if err != nil || g.State != StateLocked || g.ConfirmedAt != g0.ConfirmedAt {
		t.Fatalf("begin: %+v %v (confirmed at %s before)", g, err, g0.ConfirmedAt)
	}
}

func TestConfirmAndStartRefusalsWriteNothing(t *testing.T) {
	cases := map[string]struct {
		mod  func(*BeginRequest)
		by   tasks.Actor
		want error
	}{
		"an agent":             {func(*BeginRequest) {}, lead, tasks.ErrOperatorOnly},
		"another goal hash":    {func(r *BeginRequest) { r.GoalHash = strings.Repeat("0", 64) }, human, ErrStale},
		"no goal hash":         {func(r *BeginRequest) { r.GoalHash = "" }, human, ErrStale},
		"another chat":         {func(r *BeginRequest) { r.Chat = "chat2" }, human, ErrChatMismatch},
		"no chat":              {func(r *BeginRequest) { r.Chat = "" }, human, ErrChatMismatch},
		"another consent":      {func(r *BeginRequest) { r.Consent = strings.Repeat("0", 64) }, human, ErrConsentStale},
		"no consent":           {func(r *BeginRequest) { r.Consent = "" }, human, ErrConsentStale},
		"limits not previewed": {func(r *BeginRequest) { r.Limits = AwayLimits{Pct: 30} }, human, ErrConsentStale},
		"a limit out of range": {func(r *BeginRequest) { r.Limits = AwayLimits{Pct: 101} }, human, tasks.ErrInvalid},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newMission(t)
			mustPropose(t, f, goodCriteria(), lead)
			req := beginReq(t, f, okFacts())
			c.mod(&req)
			before := snapshot(t, f)
			_, _, err := f.store.ConfirmAndStart(bg, "default", f.session, req, c.by)
			wantIs(t, err, c.want, name)
			if after := snapshot(t, f); after != before {
				t.Errorf("a refused begin wrote: %v -> %v", before, after)
			}
		})
	}
}

func TestConfirmAndStartRefusesBlockersAndChangedFacts(t *testing.T) {
	f := newMission(t)
	mustPropose(t, f, goodCriteria(), lead)
	req := beginReq(t, f, okFacts())
	before := snapshot(t, f)
	off := okFacts()
	off.HostOn, off.BudgetKnown = false, false
	req.Live = liveOf(off)
	_, _, err := f.store.ConfirmAndStart(bg, "default", f.session, req, human)
	codes, ok := IsBlocked(err)
	if !ok || strings.Join(codes, ",") != BlockBudgetUnknown+","+BlockHostOff {
		t.Errorf("blocked = %v %v", codes, err)
	}
	changed := okFacts()
	changed.JudgePin = "claude/sonnet@4" // the judge changed after the person read the preview
	req.Live = liveOf(changed)
	_, _, err = f.store.ConfirmAndStart(bg, "default", f.session, req, human)
	wantIs(t, err, ErrConsentStale, "a changed judge")
	if after := snapshot(t, f); after != before {
		t.Errorf("refusals wrote: %v -> %v", before, after)
	}
	// a goal revised after the preview is stale by its hash
	mustPropose(t, f, otherCriteria(), lead)
	req.Live = liveOf(okFacts())
	_, _, err = f.store.ConfirmAndStart(bg, "default", f.session, req, human)
	wantIs(t, err, ErrStale, "a revised proposal")
}

func TestConfirmAndStartTextTooLongToShowWholeRefusesOnlyTheAppPath(t *testing.T) {
	f := newMission(t)
	mustPropose(t, f, []Criterion{{Kind: KindRubric, Spec: strings.Repeat("x", MaxDisplayRunes+1)}}, lead)
	req := beginReq(t, f, okFacts())
	req.GUI = true
	before := snapshot(t, f)
	_, _, err := f.store.ConfirmAndStart(bg, "default", f.session, req, human)
	wantIs(t, err, ErrCriterionTooLong, "gui with a criterion too long to show whole")
	if after := snapshot(t, f); after != before {
		t.Errorf("wrote: %v -> %v", before, after)
	}
	req.GUI = false
	if _, _, err := f.store.ConfirmAndStart(bg, "default", f.session, req, human); err != nil {
		t.Errorf("the terminal path: %v", err)
	}
}

// A failure after the confirm (here: the pursuit row cannot be inserted) rolls the confirm back: no confirmed goal without a pursuit.
func TestConfirmAndStartCannotLeaveAConfirmedGoalWithoutAPursuit(t *testing.T) {
	f := newMission(t)
	mustPropose(t, f, goodCriteria(), lead)
	req := beginReq(t, f, okFacts())
	mustExec(t, f.db, `CREATE TRIGGER test_no_pursuit BEFORE INSERT ON mission_pursuit BEGIN SELECT RAISE(ABORT, 'disk on fire'); END`)
	if _, _, err := f.store.ConfirmAndStart(bg, "default", f.session, req, human); err == nil {
		t.Fatal("the begin succeeded")
	}
	if s := goalState(t, f); s != StateProposed {
		t.Errorf("goal after a failed begin = %s, want proposed", s)
	}
}

func TestTwoConcurrentBeginsHaveOneWinner(t *testing.T) {
	f := newMission(t)
	mustPropose(t, f, goodCriteria(), lead)
	req := beginReq(t, f, okFacts())
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = f.store.ConfirmAndStart(bg, "default", f.session, req, human)
		}()
	}
	wg.Wait()
	won := 0
	for _, e := range errs {
		switch {
		case e == nil:
			won++
		case !errors.Is(e, ErrAlreadyStarted):
			t.Errorf("a loser got %v", e)
		}
	}
	if won != 1 || count(t, f.db, `SELECT COUNT(*) FROM mission_pursuit`) != 1 {
		t.Errorf("winners = %d", won)
	}
}

func TestPreviewWritesNothingAndRefusesAStartedPursuit(t *testing.T) {
	f := newMission(t)
	mustPropose(t, f, goodCriteria(), lead)
	before := snapshot(t, f)
	v, err := f.store.Preview(bg, "default", f.session, AwayLimits{}, liveOf(okFacts()))
	if err != nil || v.Goal.State != StateProposed || v.Limits.Pct != DefaultPct || v.Chat != "chat1" {
		t.Fatalf("preview: %+v %v", v, err)
	}
	if after := snapshot(t, f); after != before {
		t.Errorf("preview wrote")
	}
	f = confirmedMission(t)
	mustStart(t, f)
	_, err = f.store.Preview(bg, "default", f.session, AwayLimits{}, liveOf(okFacts()))
	wantIs(t, err, ErrAlreadyStarted, "preview after start")
}
