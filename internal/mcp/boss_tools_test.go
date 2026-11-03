package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
)

var bossToolNames = []string{"session_boss_answer_ask", "session_boss_approve_plan", "session_boss_grant_budget"}

// bossCharters, bossLedger and bossSink are the fakes under the real enforcer: the charter the run holds, the
// cumulative counters, and the audit and person-item sinks.
type bossCharters struct {
	live  bool
	askOK bool
}

func (c *bossCharters) Active(_ context.Context, profile string) (ops.Active, bool, error) {
	cons := ops.Constraints{
		SpendGrant: &ops.SpendGrant{Targets: []string{coderhost.TargetLead, coderhost.TargetChild}, MaxPerGrantMicro: 2_000_000, RunCeilingMicro: 3_000_000},
		Run:        ops.RunCaps{SpendMicro: 1, Tasks: 5},
	}
	if c.askOK {
		cons.AskAnswer = &ops.AskAnswer{AskLimits: ops.AskLimits{SpendMicro: 1, Tasks: 1, Access: "scoped", Depth: 1}, Classes: []string{coderhost.ClassAskCritical}}
	}
	return ops.Active{ID: "ch", Hash: "h", Generation: 1, Profile: profile, Constraints: cons}, c.live, nil
}

type bossLedger struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (l *bossLedger) Consume(_ context.Context, c ops.Consume) (ops.ConsumeResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ids[c.ApprovalID] = true
	return ops.ConsumeResult{Done: true}, nil
}

func (l *bossLedger) spent() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ids)
}

type bossSink struct{}

func (bossSink) Append(context.Context, ops.AuditRow) error { return nil }
func (bossSink) Park(context.Context, ops.Park) error       { return nil }

// installBossRoute stands in for the engine's installer: a real enforcer over fake charter and counters. The
// installer runs on every call; the returned counter says how often.
func installBossRoute(t *testing.T, ch *bossCharters) (*bossLedger, *int) {
	t.Helper()
	l, calls := &bossLedger{ids: map[string]bool{}}, new(int)
	SetBossRouteInstaller(func(_ context.Context, st *coderhost.Store, _ *sql.DB, _, _ string) error {
		*calls++
		e := &ops.Enforcer{Charters: ch, Facts: st.NewDoomsdayFacts(), Ledger: l, Auditor: bossSink{}, Parker: bossSink{}, Bounds: coderhost.SpendBounds()}
		st.SetDoomsdayRoute(&coderhost.DoomsdayRoute{Enforcer: e, Live: func(context.Context, string) (bool, error) { return ch.live, nil }})
		return nil
	})
	t.Cleanup(func() { SetBossRouteInstaller(nil) })
	return l, calls
}

func TestBossToolsAreServedMutatingAndTheOnlyOnesOfTheMode(t *testing.T) {
	got := map[string]tool{}
	for _, tl := range allTools() {
		if strings.Contains(tl.name, "boss") {
			got[tl.name] = tl
		}
	}
	if len(got) != len(bossToolNames) {
		t.Fatalf("boss tools = %d", len(got))
	}
	for _, n := range bossToolNames {
		if tl, ok := got[n]; !ok || !tl.mutating {
			t.Errorf("%s: present %v, mutating %v", n, ok, tl.mutating)
		}
	}
}

func askCritical(t *testing.T, m *missionChildFixture) int64 {
	t.Helper()
	t.Setenv(agenttokens.EnvMissionToken, "")
	out, err := m.as(t, m.tokM, "session_ask_ancestor", map[string]any{"question": "may we drop the table?", "importance": "critical"})
	if err != nil {
		t.Fatal(err)
	}
	var r struct {
		AskID int64 `json:"askId"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || r.AskID == 0 {
		t.Fatalf("%v %s", err, out)
	}
	return r.AskID
}

// With no installer (a default build) or no running run, every boss tool refuses and nothing is spent or answered.
func TestBossToolsRefuseWithoutAnOpenRun(t *testing.T) {
	m, _ := newAskFixture(t)
	id := askCritical(t, m)
	calls := map[string]map[string]any{
		"session_boss_answer_ask":   {"ask_id": id, "answer": "yes"},
		"session_boss_approve_plan": {"session": m.se.ID, "rev": 1, "digest": "d"},
		"session_boss_grant_budget": {"request_id": 1, "amount_usd": 1},
	}
	try := func(label string) {
		for name, args := range calls {
			t.Setenv(agenttokens.EnvMissionToken, "")
			if out, err := m.as(t, m.tokOther, name, args); err == nil || !strings.Contains(err.Error(), "not running") {
				t.Fatalf("%s %s: %v %s", label, name, err, out)
			}
		}
	}
	try("no installer")
	l, installs := installBossRoute(t, &bossCharters{live: false, askOK: true})
	try("no running run")
	if *installs == 0 {
		t.Fatal("the installer was not consulted per call")
	}
	if l.spent() != 0 {
		t.Fatalf("a refused call spent %d approvals", l.spent())
	}
	if a, err := m.store.GetAsk(m.profile, id); err != nil || a.Status != "open" {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestBossAnswerAskThroughTheRoute(t *testing.T) {
	m, _ := newAskFixture(t)
	id := askCritical(t, m)
	ch := &bossCharters{live: true, askOK: true}
	l, _ := installBossRoute(t, ch)
	args := map[string]any{"ask_id": id, "answer": "yes, drop it"}
	if _, err := m.as(t, m.tokM, "session_boss_answer_ask", args); err == nil {
		t.Fatal("the asker answered itself")
	}
	if _, err := m.as(t, m.tokA, "session_boss_answer_ask", args); err == nil {
		t.Fatal("a non-lead answered")
	}
	for _, role := range []string{agenttokens.RoleWorker, agenttokens.RoleVerifier} {
		if _, err := m.asBound(t, role, "session_boss_answer_ask", args); err == nil {
			t.Fatalf("a bound %s answered", role)
		}
	}
	t.Setenv(agenttokens.EnvMissionToken, "")
	if _, err := m.as(t, "", "session_boss_answer_ask", args); err == nil || !strings.Contains(err.Error(), "no_session") {
		t.Fatalf("no token: %v", err)
	}
	if l.spent() != 0 {
		t.Fatal("a refused answer spent an approval")
	}
	// a class the charter did not lift stays a person item
	ch.askOK = false
	if _, err := m.as(t, m.tokOther, "session_boss_answer_ask", args); err == nil {
		t.Fatalf("an unlifted class: %v", err)
	}
	if a, _ := m.store.GetAsk(m.profile, id); a.Status != "open" {
		t.Fatalf("the ask was answered: %+v", a)
	}
	ch.askOK = true
	if out, err := m.as(t, m.tokOther, "session_boss_answer_ask", args); err != nil || !strings.Contains(out, `"answered"`) {
		t.Fatalf("%v %s", err, out)
	}
	if l.spent() != 1 {
		t.Fatalf("approvals spent = %d", l.spent())
	}
	if _, err := m.as(t, m.tokOther, "session_boss_answer_ask", args); err == nil {
		t.Fatal("a second answer")
	}
}

func TestBossGrantBudgetRejectsBadAmountsBeforeTheRoute(t *testing.T) {
	m, _ := newAskFixture(t)
	l, _ := installBossRoute(t, &bossCharters{live: true})
	t.Setenv(agenttokens.EnvMissionToken, "")
	for _, a := range []any{0, -1, 1e12, "x"} {
		if _, err := m.as(t, m.tokOther, "session_boss_grant_budget", map[string]any{"request_id": 1, "amount_usd": a}); err == nil {
			t.Fatalf("amount %v accepted", a)
		}
	}
	if l.spent() != 0 {
		t.Fatal("a bad amount reached the ledger")
	}
}

func TestBossApprovePlanNeedsTheRevAndDigest(t *testing.T) {
	m, _ := newAskFixture(t)
	l, _ := installBossRoute(t, &bossCharters{live: true})
	t.Setenv(agenttokens.EnvMissionToken, "")
	if _, err := m.as(t, m.tokOther, "session_boss_approve_plan", map[string]any{"session": m.se.ID}); err == nil {
		t.Fatal("a plan approval without the rev and digest")
	}
	if l.spent() != 0 {
		t.Fatal("spent")
	}
}
