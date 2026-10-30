package tasks

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

// fakeApproval is the operator's settings: AgentLead per root conversation (an unknown one is off).
type fakeApproval struct {
	pol      map[string]ApprovalPolicy
	polErr   error
	asked    []string
	raised   map[int64]string
	raiseErr error
	onPolicy func()
}

func (f *fakeApproval) ApprovalPolicy(_ context.Context, _, root string) (ApprovalPolicy, error) {
	f.asked = append(f.asked, root)
	if f.onPolicy != nil {
		f.onPolicy()
	}
	return f.pol[root], f.polErr
}

func (f *fakeApproval) RaisePersonItem(_ context.Context, _, conv string, session int64) error {
	if f.raiseErr != nil {
		return f.raiseErr
	}
	f.raised[session] = conv
	return nil
}

func (f *fakeApproval) PersonItemRaised(_ context.Context, _ string, session int64) (bool, error) {
	_, ok := f.raised[session]
	return ok, nil
}

// tree: root R (conv-root, $100) with P (conv-p) and sibling S (conv-s); C (conv-c, draft with
// 3 tasks) below P; D (conv-d) below C.
type approvalTree struct {
	s               *Store
	db              *sql.DB
	fake            *fakeApproval
	r, p, sib, c, d Session
}

func linkConv(t *testing.T, db *sql.DB, id int64, conv string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE task_sessions SET conversation_id = ? WHERE id = ?`, conv, id); err != nil {
		t.Fatal(err)
	}
}

func newApprovalTree(t *testing.T) *approvalTree {
	t.Helper()
	s, db, _ := newTestStore(t)
	mk := func(in SessionInput) Session {
		se, err := s.CreateSession(bg, "default", in, human)
		if err != nil {
			t.Fatalf("create %q: %v", in.Title, err)
		}
		return se
	}
	a := &approvalTree{s: s, db: db}
	a.r = mk(SessionInput{Title: "root", Kind: SessionKindMission, BudgetUSD: 100})
	a.p = mk(SessionInput{Title: "p", ParentSessionID: a.r.ID, Kind: SessionKindDirection, BudgetUSD: 30, MaxTasks: 20})
	a.sib = mk(SessionInput{Title: "sib", ParentSessionID: a.r.ID, Kind: SessionKindDirection, BudgetUSD: 30, MaxTasks: 20})
	a.c = mk(SessionInput{Title: "c", ParentSessionID: a.p.ID, Kind: SessionKindTask, BudgetUSD: 10, MaxTasks: 10})
	a.d = mk(SessionInput{Title: "d", ParentSessionID: a.c.ID, Kind: SessionKindTask, BudgetUSD: 5, MaxTasks: 5})
	for id, conv := range map[int64]string{a.r.ID: "conv-root", a.p.ID: "conv-p", a.sib.ID: "conv-s", a.c.ID: "conv-c", a.d.ID: "conv-d"} {
		linkConv(t, db, id, conv)
	}
	for _, sid := range []int64{a.c.ID, a.p.ID} {
		for _, title := range []string{"one", "two", "three"} {
			mustTask(t, s, "default", sid, title)
		}
	}
	a.fake = &fakeApproval{pol: map[string]ApprovalPolicy{}, raised: map[int64]string{}}
	a.fake.pol["conv-root"] = ApprovalPolicy{AgentLead: true, CeilingUSD: 50, WorkingDepth: 3}
	s.SetApprovalSource(a.fake)
	return a
}

func leadOf(conv string) Actor { return Actor{Kind: Agent, Name: "lead-" + conv, Conversation: conv} }

func (a *approvalTree) state(t *testing.T, id int64) Session {
	t.Helper()
	se, err := a.s.GetSession(bg, "default", id, human)
	if err != nil {
		t.Fatal(err)
	}
	return se
}

// refused asserts that actor cannot approve id and that nothing changed.
func (a *approvalTree) refused(t *testing.T, id int64, actor Actor, why string) error {
	t.Helper()
	_, err := a.s.ApproveSession(bg, "default", id, actor)
	if !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("%s: want a refusal, got %v", why, err)
	}
	if se := a.state(t, id); se.State != SessionDraft || se.ApprovalBasis != "" || se.Counts.Ready != 0 {
		t.Fatalf("%s: a refused approval changed the session: %+v", why, se)
	}
	return err
}

func TestLeadOfTheParentApprovesWithBasisLead(t *testing.T) {
	a := newApprovalTree(t)
	before := a.state(t, a.c.ID)
	se, err := a.s.ApproveSession(bg, "default", a.c.ID, leadOf("conv-p"))
	if err != nil {
		t.Fatal(err)
	}
	if se.State != SessionRunning || se.ApprovalBasis != ApprovalLead || se.ApprovedBy != "lead-conv-p" || se.Counts.Ready != 3 {
		t.Fatalf("approved: %+v", se)
	}
	if se.AccessLevel != before.AccessLevel || se.BudgetUSD != before.BudgetUSD || se.MaxTasks != before.MaxTasks {
		t.Fatalf("an approval changed access, budget or caps: %+v -> %+v", before, se)
	}
	if got := strings.Join(a.fake.asked, ","); got != "conv-root" {
		t.Fatalf("the policy must come from the mission's root, asked for %q", got)
	}
	// A lead bound to its own (parent) session by an in-process binding is the same lead.
	bound := leadOf("conv-root")
	bound.Session = a.r.ID
	if _, err := a.s.ApproveSession(bg, "default", a.p.ID, bound); err != nil {
		t.Fatalf("bound to the parent: %v", err)
	}
}

func TestOperatorApprovalKeepsBasisOperatorEvenWithAgentApprovalOn(t *testing.T) {
	a := newApprovalTree(t)
	se, err := a.s.ApproveSession(bg, "default", a.c.ID, human)
	if err != nil || se.ApprovalBasis != ApprovalOperator || se.ApprovedBy != "you" {
		t.Fatalf("%+v %v", se, err)
	}
	if root, err := a.s.ApproveSession(bg, "default", a.r.ID, human); err == nil && root.ApprovalBasis != ApprovalOperator {
		t.Fatalf("root basis %q", root.ApprovalBasis)
	}
}

func TestAgentApprovalIsOffUnlessTheOperatorTurnedItOn(t *testing.T) {
	a := newApprovalTree(t)
	a.fake.pol["conv-root"] = ApprovalPolicy{AgentLead: false, WorkingDepth: 3}
	a.refused(t, a.c.ID, leadOf("conv-p"), "agentApproval off")
	// Not wired to a settings reader: no agent approves.
	b := newApprovalTree(t)
	b.s.SetApprovalSource(nil)
	b.refused(t, b.c.ID, leadOf("conv-p"), "no settings source")
	// A policy that cannot be read, or that comes with an error, is off.
	c := newApprovalTree(t)
	c.fake.polErr = errors.New("db down")
	c.refused(t, c.c.ID, leadOf("conv-p"), "unreadable settings")
	// A mission whose root is linked to no chat has no settings to read.
	d := newApprovalTree(t)
	linkConv(t, d.db, d.r.ID, "")
	d.fake.pol[""] = ApprovalPolicy{AgentLead: true, CeilingUSD: 50, WorkingDepth: 3}
	d.refused(t, d.c.ID, leadOf("conv-p"), "root without a chat")
}

func TestRootMissionSessionIsOnlyTheOperators(t *testing.T) {
	a := newApprovalTree(t)
	err := a.refused(t, a.r.ID, leadOf("conv-root"), "the root's own lead")
	if !strings.Contains(err.Error(), "root") {
		t.Fatalf("the refusal must say why: %v", err)
	}
	a.refused(t, a.r.ID, leadOf("conv-p"), "a lead below the root")
}

func TestOnlyTheLeadOfTheParentApproves(t *testing.T) {
	a := newApprovalTree(t)
	a.refused(t, a.c.ID, leadOf("conv-c"), "the session's own lead")
	a.refused(t, a.c.ID, leadOf("conv-s"), "a peer of the parent (sibling branch)")
	a.refused(t, a.c.ID, leadOf("conv-d"), "a descendant")
	a.refused(t, a.c.ID, leadOf("conv-root"), "an ancestor above the parent")
	a.refused(t, a.c.ID, leadOf("conv-nobody"), "a chat that leads nothing")
	a.refused(t, a.c.ID, Actor{Kind: Agent, Name: "lead-x"}, "no verified chat")
	a.refused(t, a.sib.ID, leadOf("conv-p"), "a lead of one branch, a session of another")
	for _, other := range []Actor{{Kind: Capture}, {}} {
		if _, err := a.s.ApproveSession(bg, "default", a.c.ID, other); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("%+v: %v", other, err)
		}
	}
}

func TestAnAgentNeverApprovesASessionLinkedToItsOwnChat(t *testing.T) {
	a := newApprovalTree(t)
	linkConv(t, a.db, a.c.ID, "conv-p") // the child shares the parent lead's chat
	a.refused(t, a.c.ID, leadOf("conv-p"), "its own session")
}

func TestApprovalBindingMustBeTheParentsAndNoTasks(t *testing.T) {
	a := newApprovalTree(t)
	for name, mod := range map[string]func(*Actor){
		"bound to the session itself": func(x *Actor) { x.Session = a.c.ID },
		"bound to a sibling":          func(x *Actor) { x.Session = a.sib.ID },
		"invalid token binding":       func(x *Actor) { x.Session = -1 },
		"bound to a task":             func(x *Actor) { x.Session, x.Task = a.p.ID, 7 },
		"verifier":                    func(x *Actor) { x.Session, x.Task, x.Verifier = a.p.ID, 7, true },
	} {
		x := leadOf("conv-p")
		mod(&x)
		a.refused(t, a.c.ID, x, name)
	}
}

func TestApprovalNeverCrossesProfiles(t *testing.T) {
	a := newApprovalTree(t)
	addProfile(t, a.db, "other")
	if _, err := a.s.ApproveSession(bg, "other", a.c.ID, leadOf("conv-p")); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("another profile's call: %v", err)
	}
	if se := a.state(t, a.c.ID); se.State != SessionDraft {
		t.Fatalf("%+v", se)
	}
}

func TestApprovalSettingsComeFromTheRootNotTheParent(t *testing.T) {
	a := newApprovalTree(t)
	// Only the parent's chat has it on: that is not the mission's setting.
	a.fake.pol = map[string]ApprovalPolicy{"conv-p": {AgentLead: true, CeilingUSD: 50, WorkingDepth: 3}}
	a.refused(t, a.c.ID, leadOf("conv-p"), "setting on the parent only")
}

// The operator's settings are read inside the write transaction: a lead that passed the
// first look is judged by the setting as it is when the approval lands. The source is asked
// while another connection cannot take the write lock.
func TestApprovalReadsTheOperatorsSettingsInsideTheWriteTransaction(t *testing.T) {
	a := newApprovalTree(t)
	var asked, inside int
	a.fake.onPolicy = func() {
		asked++
		ctx := context.Background()
		conn, err := a.db.Conn(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout = 50`); err != nil {
			t.Error(err)
			return
		}
		if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
			inside++ // the write lock is held by the approval
			return
		}
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
	}
	if _, err := a.s.ApproveSession(bg, "default", a.c.ID, leadOf("conv-p")); err != nil {
		t.Fatal(err)
	}
	if asked != 1 || inside != 1 {
		t.Fatalf("the settings must be read once, inside the write transaction: asked %d, inside %d", asked, inside)
	}
}

// The mission the lead was checked against is the one the write sees.
func TestApprovalRechecksTheMissionInsideTheTransaction(t *testing.T) {
	a := newApprovalTree(t)
	err := a.s.tx(bg, func(x dbx) error {
		se, err := a.s.sessionTx(bg, x, "default", a.c.ID)
		if err != nil {
			return err
		}
		_, err = a.s.checkLeadApproval(bg, x, "default", se, leadOf("conv-p"), "conv-elsewhere")
		return err
	})
	if !errors.Is(err, ErrOperatorOnly) || !strings.Contains(err.Error(), "mission changed") {
		t.Fatalf("root changed between the check and the write: %v", err)
	}
}

func TestApprovalOfANonDraftOrEmptySessionIsRefusedForAnAgentToo(t *testing.T) {
	a := newApprovalTree(t)
	if _, err := a.s.ApproveSession(bg, "default", a.c.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := a.s.ApproveSession(bg, "default", a.c.ID, leadOf("conv-p")); !errors.Is(err, ErrNotReady) {
		t.Fatalf("already running: %v", err)
	}
	empty, err := a.s.CreateSession(bg, "default", SessionInput{Title: "empty", ParentSessionID: a.p.ID, MaxTasks: 5, BudgetUSD: 5}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.s.ApproveSession(bg, "default", empty.ID, leadOf("conv-p")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no tasks: %v", err)
	}
}

func TestGiantSessionRaisesAPersonItemAndNeverApprovesByAnAgent(t *testing.T) {
	a := newApprovalTree(t)
	// More budget than 40% of the parent's $30 (the limit is $12).
	if _, err := a.s.UpdateSession(bg, "default", a.c.ID, SessionUpdate{BudgetUSD: ptr(25.0)}, human); err != nil {
		t.Fatal(err)
	}
	_, err := a.s.ApproveSession(bg, "default", a.c.ID, leadOf("conv-p"))
	var g *GiantError
	if !errors.As(err, &g) || !errors.Is(err, ErrOperatorOnly) || !g.Raised || len(g.Reasons) != 1 {
		t.Fatalf("want a giant refusal with an item: %v", err)
	}
	if got := a.fake.raised[a.c.ID]; got != "conv-p" {
		t.Fatalf("the item belongs to the asking lead's chat, got %q", got)
	}
	if se := a.state(t, a.c.ID); se.State != SessionDraft || se.Counts.Ready != 0 || se.ApprovalBasis != "" {
		t.Fatalf("giant work must stay a draft: %+v", se)
	}
	// An item that cannot be raised still refuses, and says so.
	a.fake.raiseErr = errors.New("no table")
	_, err = a.s.ApproveSession(bg, "default", a.c.ID, leadOf("conv-p"))
	if !errors.As(err, &g) || g.Raised || !strings.Contains(err.Error(), "no item could be raised") {
		t.Fatalf("%v", err)
	}
	// The person's answer (the person path, with the revision they read) is recorded as the person's.
	cur := a.state(t, a.c.ID)
	se, err := a.s.ApproveSessionAsPerson(bg, "default", a.c.ID, human, PlanGuard{Rev: cur.Rev})
	if err != nil || se.ApprovalBasis != ApprovalPerson || se.State != SessionRunning {
		t.Fatalf("%+v %v", se, err)
	}
	// Without an item the same path records the operator.
	a.fake.raised = map[int64]string{}
	se, err = a.s.ApproveSessionAsPerson(bg, "default", a.p.ID, human, PlanGuard{Rev: a.state(t, a.p.ID).Rev})
	if err != nil || se.ApprovalBasis != ApprovalOperator {
		t.Fatalf("%+v %v", se, err)
	}
}

// A plain `session approve` is the operator's, even while a person item waits for that session:
// only the person's own path records the person.
func TestAPlainOperatorApprovalIsRecordedAsTheOperatorsWhileAnItemWaits(t *testing.T) {
	a := newApprovalTree(t)
	a.fake.raised[a.c.ID] = "conv-p"
	se, err := a.s.ApproveSession(bg, "default", a.c.ID, human)
	if err != nil || se.ApprovalBasis != ApprovalOperator {
		t.Fatalf("plain approve with an item: %+v %v", se, err)
	}
	a2 := newApprovalTree(t)
	a2.fake.raised[a2.c.ID] = "conv-p"
	se, err = a2.s.ApproveSessionAt(bg, "default", a2.c.ID, human, PlanGuard{Rev: a2.state(t, a2.c.ID).Rev})
	if err != nil || se.ApprovalBasis != ApprovalOperator {
		t.Fatalf("approve --rev with an item: %+v %v", se, err)
	}
}

func TestGiantReasons(t *testing.T) {
	root := Session{ID: 1, BudgetUSD: 100, AccessLevel: "workspace"}
	parent := Session{ID: 2, BudgetUSD: 80, AccessLevel: "coding"}
	ok := Session{ID: 3, AccessLevel: "coding", MaxTasks: 25, BudgetUSD: 32, Depth: 3, Counts: SessionCounts{Total: 25}}
	pol := ApprovalPolicy{AgentLead: true, CeilingUSD: 50, WorkingDepth: 3}
	if r := giantReasons(ok, parent, root, pol); len(r) != 0 {
		t.Fatalf("the edge of every threshold is not giant: %v", r)
	}
	cases := map[string]struct {
		mod func(*Session, *ApprovalPolicy, *Session)
	}{
		"stored tasks over 25":       {func(s *Session, _ *ApprovalPolicy, _ *Session) { s.Counts.Total, s.Counts.Epics = 20, 6 }},
		"max_tasks over 25":          {func(s *Session, _ *ApprovalPolicy, _ *Session) { s.MaxTasks = 26 }},
		"over 40% of the parent's":   {func(s *Session, _ *ApprovalPolicy, _ *Session) { s.BudgetUSD = 32.01 }},
		"no budget under a budget":   {func(s *Session, _ *ApprovalPolicy, _ *Session) { s.BudgetUSD = 0 }},
		"no root budget, no ceiling": {func(_ *Session, p *ApprovalPolicy, r *Session) { r.BudgetUSD, p.CeilingUSD = 0, 0 }},
		"over the USD ceiling":       {func(s *Session, p *ApprovalPolicy, _ *Session) { p.CeilingUSD = 31 }},
		"no budget under a ceiling":  {func(s *Session, p *ApprovalPolicy, r *Session) { s.BudgetUSD, p.CeilingUSD, r.BudgetUSD = 0, 50, 0 }},
		"access above the parent's":  {func(s *Session, _ *ApprovalPolicy, _ *Session) { s.AccessLevel = "workspace" }},
		"unknown access level":       {func(s *Session, _ *ApprovalPolicy, _ *Session) { s.AccessLevel = "root" }},
		"deeper than the working":    {func(s *Session, p *ApprovalPolicy, _ *Session) { p.WorkingDepth = 2 }},
		"a zero working depth":       {func(s *Session, p *ApprovalPolicy, _ *Session) { p.WorkingDepth = 0 }},
		"level beyond the hard max":  {func(s *Session, p *ApprovalPolicy, _ *Session) { s.Depth, p.WorkingDepth = 5, 9 }},
	}
	for name, c := range cases {
		s, p, r := ok, pol, root
		c.mod(&s, &p, &r)
		if got := giantReasons(s, parent, r, p); len(got) == 0 {
			t.Errorf("%s must be giant", name)
		}
	}
	// A ceiling stands in for a root without a budget; a parent without one has no share to exceed.
	s, p, r, par := ok, pol, root, parent
	r.BudgetUSD, p.CeilingUSD, par.BudgetUSD = 0, 50, 0
	if got := giantReasons(s, par, r, p); len(got) != 0 {
		t.Errorf("a ceiling replaces the root's budget and an unbudgeted parent has no share: %v", got)
	}
}

// A lead's approval always has a spend limit: agent_approval=lead with no ceiling above 0 is not
// a policy an agent may act on, whatever else the mission holds (a root budget does not stand in
// for it), even if such a row got stored past the settings' validation.
func TestALeadWithoutACeilingAboveZeroNeverApproves(t *testing.T) {
	for name, ceiling := range map[string]float64{"zero": 0, "negative": -1} {
		t.Run(name, func(t *testing.T) {
			a := newApprovalTree(t)
			a.fake.pol["conv-root"] = ApprovalPolicy{AgentLead: true, CeilingUSD: ceiling, WorkingDepth: 3}
			a.refused(t, a.c.ID, leadOf("conv-p"), "lead without a ceiling")
			if len(a.fake.raised) != 0 {
				t.Fatalf("a refusal for want of a ceiling raised a person item: %v", a.fake.raised)
			}
		})
	}
}

// approval_basis 'person' is written only by the person's own path, for a human: whatever an
// agent calls (even a lead, even while the item of that very session waits) never records it,
// and every agent route into the person path is refused.
func TestAnAgentNeverWritesTheBasisPerson(t *testing.T) {
	a := newApprovalTree(t)
	a.fake.raised[a.c.ID] = "conv-p"
	lead := leadOf("conv-p")
	rev := PlanGuard{Rev: a.state(t, a.c.ID).Rev}
	if _, err := a.s.ApproveSessionAsPerson(bg, "default", a.c.ID, lead, rev); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an agent answered as the person: %v", err)
	}
	if _, err := a.s.ApproveSessionAt(bg, "default", a.c.ID, lead, rev); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an agent used the guarded operator path: %v", err)
	}
	if se := a.state(t, a.c.ID); se.State != SessionDraft || se.ApprovalBasis != "" {
		t.Fatalf("a refused route changed the session: %+v", se)
	}
	se, err := a.s.ApproveSession(bg, "default", a.c.ID, lead)
	if err != nil || se.ApprovalBasis != ApprovalLead {
		t.Fatalf("a lead's plain approval while an item waits must record the lead: %+v %v", se, err)
	}
	if n := countBasis(t, a.db, ApprovalPerson); n != 0 {
		t.Fatalf("%d sessions carry the person's basis after only agents acted", n)
	}
}

func countBasis(t *testing.T, db *sql.DB, basis string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE approval_basis = ?`, basis).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
