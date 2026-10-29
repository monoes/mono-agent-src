package coderhost

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

func TestApprovalSettingsAreOperatorOnlyAndValidated(t *testing.T) {
	if d := DefaultSettings(); d.AgentApproval != "off" || d.ApprovalCeilingUSD != 0 {
		t.Fatalf("the defaults are off: %+v", d)
	}
	for _, k := range []string{"agent_approval", "approval_ceiling_usd"} {
		if !oneOf(k, OperatorOnly) || oneOf(k, AgentAdjustable) {
			t.Errorf("%s must be operator-only and not agent-adjustable", k)
		}
	}
	d := DefaultSettings()
	for _, patch := range []string{`{"agent_approval":"on"}`, `{"agent_approval":""}`, `{"approval_ceiling_usd":-1}`, `{"approval_ceiling_usd":2000000}`} {
		if _, _, err := d.Apply([]byte(patch)); err == nil {
			t.Errorf("%s was accepted", patch)
		}
	}
	d.ApprovalCeilingUSD = math.NaN()
	if d.Validate() == nil {
		t.Error("a NaN ceiling was accepted")
	}
	got, keys, err := DefaultSettings().Apply([]byte(`{"agent_approval":"lead","approval_ceiling_usd":12.5}`))
	if err != nil || got.AgentApproval != "lead" || got.ApprovalCeilingUSD != 12.5 || len(keys) != 2 {
		t.Fatalf("%+v %v %v", got, keys, err)
	}
}

func TestAMasterNeverSetsApprovalSettingsOnADescendant(t *testing.T) {
	st := newStoreT(t)
	m, _ := master(t, st, "coding")
	a := spawn(t, st, m)
	for _, p := range []string{`{"agent_approval":"lead"}`, `{"approval_ceiling_usd":5}`, `{"checkin_minutes":5,"agent_approval":"lead"}`} {
		if _, _, err := st.SetChild(m, a, []byte(p)); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%s: %v", p, err)
		}
	}
	if got, _ := st.GetSettings("p", a); got.AgentApproval != "off" {
		t.Fatalf("a refused patch changed the setting: %+v", got)
	}
}

func TestPlanApprovalIsAGapClassWithTheUsualRouting(t *testing.T) {
	if !oneOf("plan_approval", GapClasses) || HardClass("plan_approval") {
		t.Fatalf("plan_approval is a class and not a hard one")
	}
	s := DefaultSettings()
	if d := Decide(s, "plan_approval", true, true); d.Answerer != WhoMaster {
		t.Fatalf("ask-on-risk: %+v", d)
	}
	if d := Decide(s, "plan_approval", true, false); d.Answerer != WhoUser {
		t.Fatalf("with no master the person answers: %+v", d)
	}
	s.AutonomyPreset = "ask-always"
	if d := Decide(s, "plan_approval", true, true); d.Answerer != WhoUser {
		t.Fatalf("ask-always: %+v", d)
	}
	patched, _, err := DefaultSettings().Apply([]byte(`{"autonomy_classes":{"plan_approval":{"who":"model","fallback":"take_default"}}}`))
	if err != nil || Decide(patched, "plan_approval", true, true).Answerer != WhoModel {
		t.Fatalf("a per-class override is allowed: %v", err)
	}
	if _, ok := AutonomyMatrix(s, true, true)["plan_approval"]; !ok {
		t.Fatal("the matrix shows the class")
	}
}

func TestApprovalPolicyReadsTheRootsOperatorSettings(t *testing.T) {
	st := newStoreT(t)
	ctx := context.Background()
	if _, err := st.ApprovalPolicy(ctx, "p", ""); err == nil {
		t.Fatal("a root with no chat has no settings")
	}
	if pol, err := st.ApprovalPolicy(ctx, "p", "nobody"); err != nil || pol.AgentLead || pol.WorkingDepth != 3 || pol.CeilingUSD != 0 {
		t.Fatalf("defaults are off: %+v %v", pol, err)
	}
	set, _, err := DefaultSettings().Apply([]byte(`{"agent_approval":"lead","approval_ceiling_usd":7,"max_depth":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings("p", "root", set); err != nil {
		t.Fatal(err)
	}
	pol, err := st.ApprovalPolicy(ctx, "p", "root")
	if err != nil || !pol.AgentLead || pol.CeilingUSD != 7 || pol.WorkingDepth != 1 {
		t.Fatalf("%+v %v", pol, err)
	}
	if other, _ := st.ApprovalPolicy(ctx, "other", "root"); other.AgentLead {
		t.Fatal("settings are per profile")
	}
}

// missionTree is a root (chat "root", $100) with a parent P (chat "lead-p") and a draft child C of 3 tasks.
func missionTree(t *testing.T, st *Store, rootSettings string) (c tasks.Session, ts *tasks.Store) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p', 'P')`); err != nil {
		t.Fatal(err)
	}
	if rootSettings != "" {
		set, _, err := DefaultSettings().Apply([]byte(rootSettings))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SaveSettings("p", "root", set); err != nil {
			t.Fatal(err)
		}
	}
	ts = tasks.NewStore(st.db)
	ts.SetApprovalSource(st)
	ctx := context.Background()
	mk := func(in tasks.SessionInput) tasks.Session {
		se, err := ts.CreateSession(ctx, "p", in, opActor)
		if err != nil {
			t.Fatal(err)
		}
		return se
	}
	r := mk(tasks.SessionInput{Title: "root", ConversationID: "root", Kind: tasks.SessionKindMission, BudgetUSD: 100})
	p := mk(tasks.SessionInput{Title: "p", ConversationID: "lead-p", ParentSessionID: r.ID, BudgetUSD: 30, MaxTasks: 20})
	c = mk(tasks.SessionInput{Title: "c", ParentSessionID: p.ID, BudgetUSD: 10, MaxTasks: 10})
	for _, title := range []string{"a", "b", "c"} {
		if _, err := ts.AddSessionTask(ctx, "p", c.ID, tasks.SessionTaskInput{Title: title, Acceptance: "ok"}, opActor); err != nil {
			t.Fatal(err)
		}
	}
	return c, ts
}

var approvalLeadActor = tasks.Actor{Kind: tasks.Agent, Name: "lead-p", Conversation: "lead-p"}

func TestLeadApprovalNeedsTheOperatorsSettingOnTheRoot(t *testing.T) {
	ctx := context.Background()
	st := newStoreT(t)
	c, ts := missionTree(t, st, "")
	if _, err := ts.ApproveSession(ctx, "p", c.ID, approvalLeadActor); !errors.Is(err, tasks.ErrOperatorOnly) {
		t.Fatalf("default is off: %v", err)
	}
	st2 := newStoreT(t)
	c, ts = missionTree(t, st2, `{"agent_approval":"lead","max_depth":2}`)
	// Level 3 is deeper than the operator's working depth 2: giant, so a person item.
	_, err := ts.ApproveSession(ctx, "p", c.ID, approvalLeadActor)
	var g *tasks.GiantError
	if !errors.As(err, &g) || !g.Raised {
		t.Fatalf("want giant: %v", err)
	}
}

func TestGiantApprovalIsAPersonItemThatNoTimeoutOrMatrixAnswers(t *testing.T) {
	ctx := context.Background()
	st := newStoreT(t)
	// The matrix names the model with a default for plan_approval: it changes nothing for giant work.
	c, ts := missionTree(t, st, `{"agent_approval":"lead","max_depth":2,"autonomy_classes":{"plan_approval":{"who":"model","fallback":"take_default"}}}`)
	if _, err := ts.ApproveSession(ctx, "p", c.ID, approvalLeadActor); err == nil {
		t.Fatal("giant work was approved by an agent")
	}
	st.now = func() time.Time { return time.Now().Add(10 * ParkTimeout) }
	items, err := st.OpenPersonItems("p")
	if err != nil || len(items) != 1 || items[0].Kind != ItemPlanApproval || items[0].RefID != c.ID || items[0].ConversationID != "lead-p" {
		t.Fatalf("items %+v %v", items, err)
	}
	if raised, _ := st.PersonItemRaised(ctx, "p", c.ID); !raised {
		t.Fatal("PersonItemRaised")
	}
	// Asking again raises nothing new.
	_, _ = ts.ApproveSession(ctx, "p", c.ID, approvalLeadActor)
	if items, _ = st.OpenPersonItems("p"); len(items) != 1 {
		t.Fatalf("a second ask duplicated the item: %+v", items)
	}
	// A stopped chat's item is hidden; the person's approval closes it and records the basis.
	if _, err := st.EnsureSession("p", "lead-p"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET state = ? WHERE conversation_id = 'lead-p'`, StateStopped); err != nil {
		t.Fatal(err)
	}
	if items, _ = st.OpenPersonItems("p"); len(items) != 0 {
		t.Fatalf("a stopped chat's item must be hidden: %+v", items)
	}
	if _, err := st.db.Exec(`UPDATE coder_sessions SET state = ? WHERE conversation_id = 'lead-p'`, StateIdle); err != nil {
		t.Fatal(err)
	}
	if items, _ = st.OpenPersonItems("p"); len(items) != 1 {
		t.Fatalf("the item is open again: %+v", items)
	}
	se, err := ts.ApproveSession(ctx, "p", c.ID, opActor)
	if err != nil || se.ApprovalBasis != tasks.ApprovalPerson {
		t.Fatalf("%+v %v", se, err)
	}
	if items, _ = st.OpenPersonItems("p"); len(items) != 0 {
		t.Fatalf("an approved session's item must close: %+v", items)
	}
}

func TestRaisePersonItemNeedsAChatAndASession(t *testing.T) {
	st := newStoreT(t)
	for _, c := range []struct {
		conv string
		id   int64
	}{{"", 1}, {"x", 0}} {
		if err := st.RaisePersonItem(context.Background(), "p", c.conv, c.id); err == nil {
			t.Errorf("%+v was accepted", c)
		}
	}
}
