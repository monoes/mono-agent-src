package ops

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
)

var bg = context.Background()

var bounds = SpendBounds{MinMicro: 10_000, MaxMicro: 50_000_000}

type fakeCharters struct {
	mu    sync.Mutex
	act   Active
	live  bool
	calls int
}

func (f *fakeCharters) Active(context.Context, string) (Active, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.act, f.live, nil
}

type fakeFacts struct {
	mu    sync.Mutex
	calls int
	plan  Subject
	ask   Subject
	grant GrantFacts
}

func (f *fakeFacts) Plan(context.Context, string, int64) (Subject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.plan, nil
}
func (f *fakeFacts) Ask(context.Context, string, int64) (Subject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.ask, nil
}
func (f *fakeFacts) Grant(context.Context, string, int64) (GrantFacts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.grant, nil
}

// fakeLedger is atomic under one mutex: all spends fit or none is charged; an approval id is consumed once.
type fakeLedger struct {
	mu       sync.Mutex
	used     map[string]int64
	consumed map[string]bool
	audits   []AuditRow
	failNext bool
}

func newLedger() *fakeLedger {
	return &fakeLedger{used: map[string]int64{}, consumed: map[string]bool{}}
}

func (l *fakeLedger) Consume(_ context.Context, c Consume) (ConsumeResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failNext { // the audit insert failed: the whole transaction rolls back
		l.failNext = false
		return ConsumeResult{}, errors.New("audit insert failed")
	}
	if l.consumed[c.ApprovalID] {
		return ConsumeResult{Replay: true}, nil
	}
	var over []string
	for _, s := range c.Spends {
		if l.used[s.Kind]+s.N > s.Cap {
			over = append(over, s.Kind)
		}
	}
	if len(over) > 0 {
		return ConsumeResult{Over: over}, nil
	}
	for _, s := range c.Spends {
		l.used[s.Kind] += s.N
	}
	l.consumed[c.ApprovalID] = true
	l.audits = append(l.audits, c.Audit)
	return ConsumeResult{Done: true}, nil
}

type fakeAudit struct {
	mu   sync.Mutex
	rows []AuditRow
}

func (a *fakeAudit) Append(_ context.Context, r AuditRow) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows = append(a.rows, r)
	return nil
}

type fakeParker struct {
	mu    sync.Mutex
	parks []Park
	err   error
}

func (p *fakeParker) Park(_ context.Context, x Park) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.parks = append(p.parks, x)
	return nil
}

type rig struct {
	e  *Enforcer
	c  *fakeCharters
	f  *fakeFacts
	l  *fakeLedger
	a  *fakeAudit
	pk *fakeParker
}

func goodConstraints() Constraints {
	lim := AskLimits{SpendMicro: 5_000_000, Tasks: 10, Access: "coding", Depth: 3}
	return Constraints{
		PlanApprove: &PlanApprove{lim},
		AskAnswer:   &AskAnswer{AskLimits: lim, Classes: []string{"context"}},
		SpendGrant:  &SpendGrant{Targets: []string{"lead", "child"}, MaxPerGrantMicro: 10_000_000, RunCeilingMicro: 30_000_000},
		Run:         RunCaps{SpendMicro: 12_000_000, Tasks: 25},
	}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{c: &fakeCharters{live: true, act: Active{ID: "ch1", Hash: "h1", Generation: 7, Profile: "default", Constraints: goodConstraints()}},
		f: &fakeFacts{}, l: newLedger(), a: &fakeAudit{}, pk: &fakeParker{}}
	r.f.plan = Subject{Open: true, Rev: 3, Digest: "d3", Tasks: 5, SpendMicro: 1_000_000, Access: "coding", Depth: 2}
	r.f.ask = Subject{Open: true, Class: "context", Tasks: 1, SpendMicro: 0, Access: "scoped", Depth: 1}
	r.f.grant = GrantFacts{Open: true, Target: "lead", AmountMicro: 8_000_000}
	r.e = &Enforcer{Charters: r.c, Facts: r.f, Ledger: r.l, Auditor: r.a, Parker: r.pk, Bounds: bounds, Bosses: fakeBosses{id: 4}}
	return r
}

func base(op string) Request {
	return Request{Profile: "default", CharterID: "ch1", Hash: "h1", Generation: 7, Op: op, ApprovalID: "ap1", Caller: Caller{4}, Addressed: 4}
}

func planReq(id string) Request {
	r := base(OpPlanApprove)
	r.ApprovalID = id
	r.Plan = &PlanArgs{Session: 9, Rev: 3, Digest: "d3"}
	return r
}

func answerReq(id string) Request {
	r := base(OpAskAnswer)
	r.ApprovalID = id
	r.Answer = &AnswerArgs{AskID: 2, Class: "context"}
	return r
}

func grantReq(id string, amount int64) Request {
	r := base(OpSpendGrant)
	r.ApprovalID = id
	r.Grant = &GrantArgs{RequestID: 5, Target: "lead", AmountMicro: amount}
	return r
}

func decide(t *testing.T, r *rig, req Request) Decision {
	t.Helper()
	d, err := r.e.Decide(bg, req)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	return d
}

func TestNoCharterIsDefaultDenyAndTouchesNothing(t *testing.T) {
	r := newRig(t)
	r.c.live = false
	for _, req := range []Request{planReq("a"), answerReq("b"), grantReq("c", 1_000_000)} {
		if _, err := r.e.Decide(bg, req); !errors.Is(err, ErrNoCharter) {
			t.Fatalf("%s: %v", req.Op, err)
		}
	}
	if r.f.calls != 0 || len(r.l.consumed) != 0 || len(r.pk.parks) != 0 || len(r.a.rows) != 0 {
		t.Fatalf("a refused request touched the store: facts %d ledger %v parks %v", r.f.calls, r.l.consumed, r.pk.parks)
	}
}

func TestOperationsOffTheAllowlistAreDenied(t *testing.T) {
	r := newRig(t)
	for _, op := range []string{"vault.use", "send.third_party", "local.destructive", "host.setting", "install.package", "", "credential.grant", "stop.resume"} {
		req := base(op)
		if _, err := r.e.Decide(bg, req); !errors.Is(err, ErrNotAllowlisted) {
			t.Fatalf("%q: %v", op, err)
		}
	}
	if r.c.calls != 0 {
		t.Fatal("the allowlist is checked before the charter is read")
	}
}

func TestALineWithoutConstraintsEnablesNothing(t *testing.T) {
	for _, op := range []string{OpPlanApprove, OpAskAnswer, OpSpendGrant} {
		r := newRig(t)
		switch op {
		case OpPlanApprove:
			r.c.act.Constraints.PlanApprove = nil
		case OpAskAnswer:
			r.c.act.Constraints.AskAnswer = nil
		default:
			r.c.act.Constraints.SpendGrant = nil
		}
		req := map[string]Request{OpPlanApprove: planReq("a"), OpAskAnswer: answerReq("a"), OpSpendGrant: grantReq("a", 1_000_000)}[op]
		if _, err := r.e.Decide(bg, req); !errors.Is(err, ErrNotEnabled) {
			t.Fatalf("%s: %v", op, err)
		}
		if r.f.calls != 0 || len(r.l.consumed) != 0 {
			t.Fatalf("%s: read or spent something", op)
		}
	}
}

func TestStaleCharterGenerationProfileOrHashIsRefused(t *testing.T) {
	for name, mut := range map[string]func(*Request){
		"hash":       func(q *Request) { q.Hash = "other" },
		"generation": func(q *Request) { q.Generation = 6 },
		"charter":    func(q *Request) { q.CharterID = "ch0" },
		"profile":    func(q *Request) { q.Profile = "other" },
	} {
		r := newRig(t)
		req := planReq("a")
		mut(&req)
		if _, err := r.e.Decide(bg, req); !errors.Is(err, ErrCharterStale) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestCallerComesFromTheTokenAndMustBeTheAddressedSession(t *testing.T) {
	r := newRig(t)
	req := planReq("a")
	req.Caller = Caller{5}
	if _, err := r.e.Decide(bg, req); !errors.Is(err, ErrCallerMismatch) {
		t.Fatalf("another session presented it: %v", err)
	}
	req.Caller = Caller{}
	req.Addressed = 0
	if _, err := r.e.Decide(bg, req); !errors.Is(err, ErrCallerMismatch) {
		t.Fatalf("no caller: %v", err)
	}
	if len(r.l.consumed) != 0 {
		t.Fatal("spent")
	}
}

func TestAPlanJustInsideEveryLimitIsApprovedAndAudited(t *testing.T) {
	r := newRig(t)
	r.f.plan = Subject{Open: true, Rev: 3, Digest: "d3", Tasks: 10, SpendMicro: 5_000_000, Access: "coding", Depth: 3}
	d := decide(t, r, planReq("a"))
	if d.Verdict != Approved || d.ArgsHash == "" {
		t.Fatalf("%+v", d)
	}
	if len(r.l.audits) != 1 || r.l.audits[0].Result != "approved" || r.l.audits[0].Limits.PlanApprove.SpendMicro != 5_000_000 || r.l.audits[0].Actual.Tasks != 10 {
		t.Fatalf("the audit row must carry the limits and the actual values: %+v", r.l.audits)
	}
	if len(r.pk.parks) != 0 {
		t.Fatal("parked")
	}
}

func TestAPlanJustOverEachLimitParksAndSpendsNothing(t *testing.T) {
	cases := map[string]struct {
		mut  func(*Subject)
		want string
	}{
		"spend":          {func(s *Subject) { s.SpendMicro = 5_000_001 }, LimitAskSpend},
		"tasks":          {func(s *Subject) { s.Tasks = 11 }, LimitAskTasks},
		"access":         {func(s *Subject) { s.Access = "workspace" }, LimitAskAccess},
		"depth":          {func(s *Subject) { s.Depth = 4 }, LimitAskDepth},
		"full":           {func(s *Subject) { s.Access = "full" }, LimitNeverLiftable},
		"never":          {func(s *Subject) { s.NeverLiftable = true }, LimitNeverLiftable},
		"unknown access": {func(s *Subject) { s.Access = "root" }, LimitNeverLiftable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.f.plan = Subject{Open: true, Rev: 3, Digest: "d3", Tasks: 10, SpendMicro: 5_000_000, Access: "coding", Depth: 3}
			tc.mut(&r.f.plan)
			d := decide(t, r, planReq("a"))
			if d.Verdict != Parked || !slices.Contains(d.Exceeded, tc.want) {
				t.Fatalf("%+v", d)
			}
			if len(r.l.consumed) != 0 || len(r.l.used) != 0 {
				t.Fatalf("a parked ask spent: %v", r.l.used)
			}
			if len(r.pk.parks) != 1 || len(r.a.rows) != 1 || r.a.rows[0].Result != "parked" || !slices.Contains(r.a.rows[0].Exceeded, tc.want) {
				t.Fatalf("park %v audit %+v", r.pk.parks, r.a.rows)
			}
		})
	}
}

func TestAFullAccessCeilingIsNeverAutoApprovableEvenAtItsOwnValue(t *testing.T) {
	r := newRig(t)
	r.c.act.Constraints.PlanApprove.Access = "full"
	if _, err := r.e.Decide(bg, planReq("a")); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("a charter with ask_access full must not enable the operation: %v", err)
	}
}

func TestAPlanThatChangedAfterTheAnswerIsRefused(t *testing.T) {
	for name, mut := range map[string]func(*Subject){
		"rev":    func(s *Subject) { s.Rev = 4 },
		"digest": func(s *Subject) { s.Digest = "d4" },
	} {
		r := newRig(t)
		mut(&r.f.plan)
		if _, err := r.e.Decide(bg, planReq("a")); !errors.Is(err, ErrPlanChanged) {
			t.Fatalf("%s: %v", name, err)
		}
		if len(r.l.consumed) != 0 {
			t.Fatal("spent on a changed plan")
		}
	}
	r := newRig(t)
	r.f.plan.Open = false
	if _, err := r.e.Decide(bg, planReq("a")); !errors.Is(err, ErrNotOpen) {
		t.Fatalf("closed plan: %v", err)
	}
}

func TestTheActualValuesComeFromTheStoreNotTheRequest(t *testing.T) {
	r := newRig(t)
	r.f.plan.Tasks = 500
	req := planReq("a")
	// nothing in the request can carry a count; the arguments type has no such field, the store says 500
	if d := decide(t, r, req); d.Verdict != Parked {
		t.Fatalf("%+v", d)
	}
}

func TestManySmallAsksParkAtTheCrossingAsk(t *testing.T) {
	r := newRig(t) // run cap: 12_000_000 micro, 25 tasks
	r.f.plan = Subject{Open: true, Rev: 3, Digest: "d3", Tasks: 1, SpendMicro: 4_000_000, Access: "scoped", Depth: 1}
	for i := 0; i < 3; i++ {
		if d := decide(t, r, planReq(fmt.Sprintf("a%d", i))); d.Verdict != Approved {
			t.Fatalf("ask %d: %+v", i, d)
		}
	}
	d := decide(t, r, planReq("a3"))
	if d.Verdict != Parked || !slices.Contains(d.Exceeded, LimitRunSpend) {
		t.Fatalf("the crossing ask: %+v", d)
	}
	if r.l.used[KindAskSpend] != 12_000_000 || r.l.used[KindAskTasks] != 3 {
		t.Fatalf("the parked ask was charged: %v", r.l.used)
	}
}

func TestASplitAskCountsAsTheSumOfItsParts(t *testing.T) {
	r := newRig(t)
	r.f.plan = Subject{Open: true, Rev: 3, Digest: "d3", Tasks: 9, SpendMicro: 0, Access: "scoped", Depth: 1}
	approved := 0
	for i := 0; i < 4; i++ { // 4 x 9 = 36 tasks against a run cap of 25
		if decide(t, r, planReq(fmt.Sprintf("p%d", i))).Verdict == Approved {
			approved++
		}
	}
	if approved != 2 || r.l.used[KindAskTasks] != 18 {
		t.Fatalf("approved %d, used %v", approved, r.l.used)
	}
}

func TestConcurrentAsksRaceForTheLastUnit(t *testing.T) {
	r := newRig(t)
	r.f.plan = Subject{Open: true, Rev: 3, Digest: "d3", Tasks: 5, SpendMicro: 0, Access: "scoped", Depth: 1}
	var wg sync.WaitGroup
	var mu sync.Mutex
	approved := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, err := r.e.Decide(bg, planReq(fmt.Sprintf("c%d", i)))
			if err != nil {
				t.Error(err)
				return
			}
			if d.Verdict == Approved {
				mu.Lock()
				approved++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if approved != 5 || r.l.used[KindAskTasks] != 25 { // 25 tasks / 5 per ask
		t.Fatalf("approved %d, used %v", approved, r.l.used)
	}
}

func TestARetryOfTheSameApprovalIsNotCountedTwice(t *testing.T) {
	r := newRig(t)
	decide(t, r, planReq("same"))
	if _, err := r.e.Decide(bg, planReq("same")); !errors.Is(err, ErrAlreadyConsumed) {
		t.Fatalf("retry: %v", err)
	}
	if r.l.used[KindAskTasks] != 5 {
		t.Fatalf("double counted: %v", r.l.used)
	}
}

func TestAFailedConsumeLeavesNothingSpentOrApproved(t *testing.T) {
	r := newRig(t)
	r.l.failNext = true
	if d, err := r.e.Decide(bg, planReq("a")); err == nil || d.Verdict == Approved {
		t.Fatalf("%+v %v", d, err)
	}
	if len(r.l.consumed) != 0 || len(r.l.used) != 0 || len(r.l.audits) != 0 {
		t.Fatal("a failed audit insert must leave no consume and no counter")
	}
	if d := decide(t, r, planReq("a")); d.Verdict != Approved {
		t.Fatalf("the approval is unspent and usable: %+v", d)
	}
}

func TestParkFailureFailsClosed(t *testing.T) {
	r := newRig(t)
	r.f.plan.Tasks = 99
	r.pk.err = errors.New("no person items")
	if d, err := r.e.Decide(bg, planReq("a")); err == nil || d.Verdict != 0 {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestLimitsOfOneRunDoNotApplyToAnother(t *testing.T) {
	r := newRig(t)
	r.f.plan = Subject{Open: true, Rev: 3, Digest: "d3", Tasks: 20, SpendMicro: 0, Access: "scoped", Depth: 1}
	r.c.act.Constraints.PlanApprove.Tasks = 20
	if d := decide(t, r, planReq("a")); d.Verdict != Approved {
		t.Fatalf("%+v", d)
	}
	r2 := newRig(t) // a second run has its own ledger
	r2.f.plan = r.f.plan
	r2.c.act.Constraints.PlanApprove.Tasks = 20
	if d := decide(t, r2, planReq("a")); d.Verdict != Approved {
		t.Fatalf("%+v", d)
	}
}

func TestAskAnswerOnlyForLiftedClasses(t *testing.T) {
	r := newRig(t)
	if d := decide(t, r, answerReq("a")); d.Verdict != Approved {
		t.Fatalf("%+v", d)
	}
	r = newRig(t)
	r.f.ask.Class = "legal"
	req := answerReq("a")
	req.Answer.Class = "legal"
	d := decide(t, r, req)
	if d.Verdict != Parked || !slices.Contains(d.Exceeded, LimitClass) {
		t.Fatalf("a class the charter did not lift must park: %+v", d)
	}
	r = newRig(t)
	req = answerReq("a")
	req.Answer.Class = "legal" // the boss names a class that is not the ask's
	if _, err := r.e.Decide(bg, req); !errors.Is(err, ErrBadArguments) {
		t.Fatalf("class mismatch: %v", err)
	}
}

func TestSpendGrantWithinTheBudgetRequestBounds(t *testing.T) {
	r := newRig(t)
	if d := decide(t, r, grantReq("g1", 8_000_000)); d.Verdict != Approved {
		t.Fatalf("%+v", d)
	}
	// the host's own bounds: below the minimum, above the maximum, negative: refused, not parked
	for _, amt := range []int64{0, -1, bounds.MinMicro - 1, bounds.MaxMicro + 1} {
		r := newRig(t)
		r.f.grant.AmountMicro = 100_000_000
		if _, err := r.e.Decide(bg, grantReq("g", amt)); !errors.Is(err, ErrBadArguments) {
			t.Fatalf("%d: %v", amt, err)
		}
	}
	// over the charter's per-grant limit parks
	r = newRig(t)
	r.f.grant.AmountMicro = 20_000_000
	if d := decide(t, r, grantReq("g", 10_000_001)); d.Verdict != Parked || !slices.Contains(d.Exceeded, LimitGrantMax) {
		t.Fatalf("%+v", d)
	}
	// a target the charter did not list parks
	r = newRig(t)
	r.c.act.Constraints.SpendGrant.Targets = []string{"child"}
	if d := decide(t, r, grantReq("g", 1_000_000)); d.Verdict != Parked || !slices.Contains(d.Exceeded, LimitGrantTarget) {
		t.Fatalf("%+v", d)
	}
	// the grant must match the open request
	r = newRig(t)
	r.f.grant.AmountMicro = 500_000
	if _, err := r.e.Decide(bg, grantReq("g", 1_000_000)); !errors.Is(err, ErrBadArguments) {
		t.Fatalf("grant above the request: %v", err)
	}
}

func TestSpendGrantRunCeilingIsCumulative(t *testing.T) {
	r := newRig(t) // ceiling 30M, per grant 10M
	approved := 0
	for i := 0; i < 5; i++ {
		if decide(t, r, grantReq(fmt.Sprintf("g%d", i), 8_000_000)).Verdict == Approved {
			approved++
		}
	}
	if approved != 3 || r.l.used[KindGrantSpend] != 24_000_000 {
		t.Fatalf("approved %d used %v", approved, r.l.used)
	}
}

func TestConstraintValidationAtEntry(t *testing.T) {
	bad := map[string]func(*Constraints){
		"no tasks":        func(c *Constraints) { c.PlanApprove.Tasks = 0 },
		"depth 13":        func(c *Constraints) { c.PlanApprove.Depth = 13 },
		"depth 0":         func(c *Constraints) { c.AskAnswer.Depth = 0 },
		"access full":     func(c *Constraints) { c.AskAnswer.Access = "full" },
		"access unknown":  func(c *Constraints) { c.PlanApprove.Access = "" },
		"negative spend":  func(c *Constraints) { c.PlanApprove.SpendMicro = -1 },
		"above run cap":   func(c *Constraints) { c.PlanApprove.Tasks = 26 },
		"no run cap":      func(c *Constraints) { c.Run = RunCaps{} },
		"no classes":      func(c *Constraints) { c.AskAnswer.Classes = nil },
		"no targets":      func(c *Constraints) { c.SpendGrant.Targets = nil },
		"grant over max":  func(c *Constraints) { c.SpendGrant.MaxPerGrantMicro = bounds.MaxMicro + 1 },
		"grant under min": func(c *Constraints) { c.SpendGrant.MaxPerGrantMicro = bounds.MinMicro - 1 },
		"ceiling < grant": func(c *Constraints) { c.SpendGrant.RunCeilingMicro = 1 },
	}
	if err := goodConstraints().Validate(bounds); err != nil {
		t.Fatal(err)
	}
	for name, mut := range bad {
		c := goodConstraints()
		mut(&c)
		if err := c.Validate(bounds); !errors.Is(err, ErrInvalidLimits) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := (Constraints{}).Validate(bounds); err != nil {
		t.Fatalf("a charter with none of these lines is valid and enables nothing: %v", err)
	}
}

func TestInvalidConstraintsDecideNothing(t *testing.T) {
	r := newRig(t)
	r.c.act.Constraints.PlanApprove.Tasks = 0
	if _, err := r.e.Decide(bg, planReq("a")); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("%v", err)
	}
}

func TestArgsHashIsBoundToOperationAndArguments(t *testing.T) {
	a, _ := ArgsHash(OpPlanApprove, PlanArgs{Session: 1, Rev: 2, Digest: "x"})
	b, _ := ArgsHash(OpPlanApprove, PlanArgs{Session: 1, Rev: 2, Digest: "x"})
	c, _ := ArgsHash(OpPlanApprove, PlanArgs{Session: 1, Rev: 3, Digest: "x"})
	d, _ := ArgsHash(OpAskAnswer, PlanArgs{Session: 1, Rev: 2, Digest: "x"})
	if a != b || a == c || a == d || len(a) != 64 {
		t.Fatalf("%s %s %s %s", a, b, c, d)
	}
}
