package ops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// fakeStore overrides the methods the adapter uses; any other call panics on the nil embedded interface.
type fakeStore struct {
	store.Store
	gen     int64
	run     store.Run
	runErr  error
	charter store.Charter
	used    map[string]int64
	ids     map[string]bool
	audits  []string
}

func (f *fakeStore) NextGeneration(context.Context, string) (int64, error) { return f.gen + 1, nil }
func (f *fakeStore) GetRun(context.Context, string, int64) (store.Run, error) {
	return f.run, f.runErr
}
func (f *fakeStore) LoadCharter(context.Context, string, int64) (store.Charter, error) {
	return f.charter, nil
}
func (f *fakeStore) ConsumeCumulative(_ context.Context, _ string, _ int64, kind, id string, n, limit int64) error {
	if f.ids[id] {
		return store.ErrAlreadyConsumed
	}
	if f.used[kind]+n > limit {
		return store.ErrOverLimit
	}
	f.used[kind] += n
	f.ids[id] = true
	return nil
}
func (f *fakeStore) AppendAudit(_ context.Context, _ string, _ int64, kind, _ string) (store.AuditRow, error) {
	f.audits = append(f.audits, kind)
	return store.AuditRow{}, nil
}

func newFake() *fakeStore {
	return &fakeStore{gen: 3, used: map[string]int64{}, ids: map[string]bool{},
		run: store.Run{Profile: "default", Generation: 3, CharterID: "dd-x", State: store.StateRunning},
		charter: store.Charter{Profile: "default", Generation: 3,
			Limits: store.Limits{BudgetMicro: 12_000_000, MaxTotal: 25},
			Lines:  []string{store.LineSpending},
			PerAsk: store.PerAsk{Declared: true, SpendMicro: 5_000_000, Tasks: 10, Access: "coding", Depth: 3}}}
}

func TestAdapterActiveOnlyForARunningRun(t *testing.T) {
	f := newFake()
	a := StoreAdapter{Store: f, Bounds: bounds}
	act, ok, err := a.Active(bg, "default")
	if err != nil || !ok || act.Generation != 3 || act.ID != "dd-x" || len(act.Hash) != 64 {
		t.Fatalf("%+v %v %v", act, ok, err)
	}
	if err := act.Constraints.Validate(bounds); err != nil {
		t.Fatalf("derived constraints must validate: %v", err)
	}
	if act.Constraints.PlanApprove == nil || act.Constraints.SpendGrant == nil || act.Constraints.AskAnswer != nil {
		t.Fatalf("%+v", act.Constraints)
	}
	for _, st := range []store.RunState{store.StateStopRequested, store.StateAuthorityRevoked, store.StateCleanupPending, store.StateQuiescent} {
		f.run.State = st
		if _, ok, _ := a.Active(bg, "default"); ok {
			t.Fatalf("%s is not an active charter", st)
		}
	}
	f.run.State = store.StateRunning
	f.runErr = store.ErrUnverifiable
	if _, ok, err := a.Active(bg, "default"); ok || !errors.Is(err, store.ErrUnverifiable) {
		t.Fatalf("an unverifiable run must be an error: %v %v", ok, err)
	}
	f.runErr, f.gen = nil, 0
	if _, ok, err := a.Active(bg, "default"); ok || err != nil {
		t.Fatalf("no run: %v %v", ok, err)
	}
}

func TestAdapterEnablesOnlyWhatTheCharterDeclares(t *testing.T) {
	f := newFake()
	f.charter.PerAsk = store.PerAsk{}
	f.charter.Lines = nil
	act, _, _ := StoreAdapter{Store: f, Bounds: bounds}.Active(bg, "default")
	if act.Constraints.PlanApprove != nil || act.Constraints.AskAnswer != nil || act.Constraints.SpendGrant != nil {
		t.Fatalf("a charter without per-ask limits and without the spending line enables nothing: %+v", act.Constraints)
	}
}

func TestAdapterConsumeMapsTheStoresAnswers(t *testing.T) {
	f := newFake()
	a := StoreAdapter{Store: f, Bounds: bounds}
	c := Consume{Profile: "default", Generation: 3, ApprovalID: "ap", Spends: []Spend{{KindAskSpend, 4, 10}, {KindAskTasks, 2, 3}}, Audit: AuditRow{Profile: "default", Generation: 3, Op: OpPlanApprove, Result: "approved"}}
	if r, err := a.Consume(bg, c); err != nil || !r.Done || len(f.audits) != 1 || !strings.HasPrefix(f.audits[0], "ops.plan.approve") {
		t.Fatalf("%+v %v %v", r, err, f.audits)
	}
	if r, _ := a.Consume(bg, c); !r.Replay || r.Done {
		t.Fatalf("a second presentation must be a replay: %+v", r)
	}
	c.ApprovalID = "ap2"
	c.Spends[0].N = 7 // 4+7 > 10
	if r, _ := a.Consume(bg, c); r.Done || len(r.Over) != 1 || r.Over[0] != KindAskSpend {
		t.Fatalf("%+v", r)
	}
	if len(f.audits) != 1 {
		t.Fatal("an over-limit consume wrote an approved audit row")
	}
	// the store's own failure is an error, not an approval
	f2 := newFake()
	boom := &errStore{fakeStore: f2}
	if r, err := (StoreAdapter{Store: boom}).Consume(bg, c); err == nil || r.Done {
		t.Fatalf("%+v %v", r, err)
	}
}

type errStore struct{ *fakeStore }

func (errStore) ConsumeCumulative(context.Context, string, int64, string, string, int64, int64) error {
	return &store.Refusal{Reason: "the run is not running"}
}

func TestEndToEndOverTheAdapter(t *testing.T) {
	f := newFake()
	a := StoreAdapter{Store: f, Bounds: bounds, AnswerClasses: []string{"context"}}
	act, _, _ := a.Active(bg, "default")
	facts := &fakeFacts{plan: Subject{Open: true, Rev: 1, Digest: "d", Tasks: 5, SpendMicro: 1_000_000, Access: "coding", Depth: 2}}
	e := &Enforcer{Charters: a, Facts: facts, Ledger: a, Auditor: a, Parker: &fakeParker{}, Bounds: bounds}
	req := Request{Profile: "default", CharterID: act.ID, Hash: act.Hash, Generation: 3, Op: OpPlanApprove, ApprovalID: "ap1",
		Caller: Caller{4}, Addressed: 4, Plan: &PlanArgs{Session: 9, Rev: 1, Digest: "d"}}
	d, err := e.Decide(bg, req)
	if err != nil || d.Verdict != Approved || f.used[KindAskTasks] != 5 {
		t.Fatalf("%+v %v %v", d, err, f.used)
	}
	f.run.State = store.StateStopRequested
	if _, err := e.Decide(bg, req); !errors.Is(err, ErrNoCharter) {
		t.Fatalf("after Stop: %v", err)
	}
}
