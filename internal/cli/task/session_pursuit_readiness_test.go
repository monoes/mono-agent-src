package task

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// readyFacts is a world in which nothing is missing but the person's own consent.
func readyInput(mut func(in *readinessInput, f *missionpursuit.LiveFacts)) readinessInput {
	f := missionpursuit.LiveFacts{HostOn: true, JudgePin: "claude/haiku@4", WakeCapUSD: 2, BudgetKnown: true, LeadCapped: true}
	in := readinessInput{Session: 7, HostFound: true, HostAlive: true, Host: coderhost.PursuitHostInfo{Enabled: true, Pin: "claude/haiku@4", WakeCapUSD: 2}}
	in.View = missionpursuit.Readiness{Chat: "chat1", GoalFound: true, Limits: missionpursuit.AwayLimits{ParkedMax: 3, Pct: 20, Wakes: 6}}
	in.View.Goal = missionpursuit.Goal{State: "proposed", Hash: strings.Repeat("a", 64),
		Criteria: []missionpursuit.StoredCriterion{{Criterion: missionpursuit.Criterion{Kind: missionpursuit.KindRubric, Spec: "it works"}}}}
	if mut != nil {
		mut(&in, &f)
	}
	in.View.Consent = missionpursuit.ConsentOf(in.Session, in.View.Chat, in.View.Goal, in.View.Limits, f)
	return in
}

func itemOf(t *testing.T, d readinessDoc, id string) readinessItem {
	t.Helper()
	for _, it := range d.Items {
		if it.ID == id {
			return it
		}
	}
	t.Fatalf("no item %q in %+v", id, d.Items)
	return readinessItem{}
}

func TestReadinessItemsEachNameTheirExactFix(t *testing.T) {
	t.Parallel()
	cmdCrit := missionpursuit.StoredCriterion{Criterion: missionpursuit.Criterion{Kind: "command", Spec: "go test ./..."}}
	cases := []struct {
		name    string
		mut     func(*readinessInput, *missionpursuit.LiveFacts)
		item    string
		status  string
		detail  string // a word the detail must carry
		fix     string // a word the fix must carry
		blocker string
	}{
		{"all set", nil, ItemHostLoop, itemOK, "pursuit loop on", "", ""},
		{"host not running", func(in *readinessInput, f *missionpursuit.LiveFacts) { in.HostAlive, f.HostOn = false, false }, ItemHostLoop, itemMissing, "not running", "MONOAGENT_PURSUIT_JUDGE", "host-off"},
		{"no host published", func(in *readinessInput, f *missionpursuit.LiveFacts) {
			in.HostFound, in.HostAlive, f.HostOn = false, false, false
		}, ItemHostLoop, itemMissing, "no coder host", "coder host run", "host-off"},
		{"host running, loop off", func(in *readinessInput, f *missionpursuit.LiveFacts) { in.Host.Enabled, f.HostOn = false, false }, ItemHostLoop, itemMissing, "loop OFF", "coder host stop", "host-off"},
		{"host info invalid", func(in *readinessInput, f *missionpursuit.LiveFacts) { f.HostInvalid = true }, ItemHostLoop, itemMissing, "not a usable value", "restart", "host-info-invalid"},
		{"judge is the lead's", func(in *readinessInput, f *missionpursuit.LiveFacts) { f.PinWhy = "the judge's model is the lead's" }, ItemJudgePin, itemMissing, "is the lead's", "other than the lead's", "judge-pin"},
		{"judge unknown while the host is off", func(in *readinessInput, f *missionpursuit.LiveFacts) { f.HostOn = false }, ItemJudgePin, itemMissing, "until the host loop is on", "host_loop", "host-off"},
		{"no goal", func(in *readinessInput, f *missionpursuit.LiveFacts) {
			in.View.GoalFound, in.View.Goal = false, missionpursuit.Goal{}
		}, ItemGoal, itemMissing, "no goal yet", "mission_goal_propose", "no-rubric"},
		{"goal without a rubric", func(in *readinessInput, f *missionpursuit.LiveFacts) {
			in.View.Goal.Criteria = []missionpursuit.StoredCriterion{cmdCrit}
			f.CheckerSetUp = true
		}, ItemGoal, itemMissing, "no rubric criterion", "goal propose 7 --criteria", "no-rubric"},
		{"command checks and no checker", func(in *readinessInput, f *missionpursuit.LiveFacts) {
			in.View.Goal.Criteria = append(in.View.Goal.Criteria, cmdCrit)
			in.Host.VerifierReason = "no scratch folder"
		}, ItemChecker, itemMissing, "no scratch folder", "MONOAGENT_PURSUIT_VERIFIER", ""},
		{"command checks and a checker", func(in *readinessInput, f *missionpursuit.LiveFacts) {
			in.View.Goal.Criteria = append(in.View.Goal.Criteria, cmdCrit)
			f.CheckerSetUp = true
		}, ItemChecker, itemOK, "goal checker", "", ""},
		{"rubric only needs no checker", nil, ItemChecker, itemOK, "no command checks", "", ""},
		{"budget unknown", func(in *readinessInput, f *missionpursuit.LiveFacts) { f.BudgetKnown = false }, ItemMissionBudget, itemMissing, "no known budget", "task session budget 7 --usd", "budget-unknown"},
		{"lead uncapped", func(in *readinessInput, f *missionpursuit.LiveFacts) { f.LeadCapped = false }, ItemLeadCap, itemMissing, "no spend cap", "coder sessions settings set chat1 --budget-usd", "lead-uncapped"},
		{"no chat", func(in *readinessInput, f *missionpursuit.LiveFacts) { in.View.Chat = "" }, ItemChat, itemMissing, "no chat", "mission chat", "no-chat"},
		{"consent pending", nil, ItemConsent, itemMissing, "yours alone", "pursuit preview 7", ""},
		{"away limits are always shown", nil, ItemAwayLimits, itemOK, "3 parked question(s), 20%", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			doc := buildReadiness(readyInput(tc.mut))
			it := itemOf(t, doc, tc.item)
			if it.Status != tc.status || !strings.Contains(it.Detail, tc.detail) || !strings.Contains(it.Fix, tc.fix) || (tc.status == itemOK && it.Fix != "") {
				t.Errorf("%s = %+v, want status %s, detail with %q, fix with %q", tc.item, it, tc.status, tc.detail, tc.fix)
			}
			if tc.blocker != "" && !hasBlocker(doc.Blockers, tc.blocker) {
				t.Errorf("blockers %v lack %s", doc.Blockers, tc.blocker)
			}
		})
	}
}

// ready means every item but the person's consent is ok, and every missing item names a fix: the checklist is never a dead end.
func TestReadinessReadyAndNoMissingItemWithoutAFix(t *testing.T) {
	t.Parallel()
	if d := buildReadiness(readyInput(nil)); !d.Ready || len(d.Blockers) != 0 {
		t.Errorf("a complete world: ready %v blockers %v", d.Ready, d.Blockers)
	}
	d := buildReadiness(readyInput(func(in *readinessInput, f *missionpursuit.LiveFacts) {
		in.HostAlive, f.HostOn, f.BudgetKnown, f.LeadCapped, in.View.GoalFound, in.View.Goal = false, false, false, false, false, missionpursuit.Goal{}
		in.View.Chat = ""
	}))
	if d.Ready {
		t.Error("a world that lacks everything is ready")
	}
	for _, it := range d.Items {
		if it.Status == itemMissing && it.Fix == "" {
			t.Errorf("%s is missing and names no fix", it.ID)
		}
	}
}

// A begun pursuit reports its state, why it waits and who acts from the same functions as `pursuit status`.
func TestReadinessOfABegunPursuitCarriesItsWaitingReason(t *testing.T) {
	t.Parallel()
	in := readyInput(nil)
	in.View.Pursuit = &missionpursuit.Pursuit{LockedBy: "you", ParkedMax: 3, Pct: 20, WakesMax: 6}
	in.Progress = missionpursuit.Progress{Seen: true, Waiting: "spend"}
	d := buildReadiness(in)
	if p := d.Pursuit; !p.Started || p.State != "waiting" || p.Needs != "you" || p.Waiting != "spend" || p.Next != missionpursuit.NextLine(false, "spend", true, true) {
		t.Errorf("pursuit block = %+v", p)
	}
	if it := itemOf(t, d, ItemConsent); it.Status != itemOK || !d.Ready {
		t.Errorf("consent of a begun pursuit: %+v ready %v", it, d.Ready)
	}
	if buildReadiness(readyInput(nil)).Pursuit.Started {
		t.Error("a pursuit that did not begin is reported as started")
	}
}
