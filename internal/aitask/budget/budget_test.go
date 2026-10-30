package budget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

func TestMain(m *testing.M) { testhome.Main(m) }

type rig struct {
	t     *testing.T
	l     *Ledger
	store *aitask.Store
	turns int
}

func newRig(t *testing.T, operator float64) *rig {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &rig{t: t, l: &Ledger{DB: db.DB, Operator: operator}, store: &aitask.Store{DB: db.DB, Profile: "p1"}}
}

// task inserts a task in conversation conv (the conversation row too) and
// returns the admission it would ask for.
func (r *rig) task(conv, kind string, budget float64) Admission {
	r.t.Helper()
	id := aitask.NewID()
	if _, err := r.l.DB.Exec(`INSERT OR IGNORE INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES (?, 'p1', 'coder', 'x', 'x')`, conv); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.store.Insert(aitask.Task{ID: id, Kind: kind, ConversationID: conv, Status: aitask.StatusRunning, Goal: "g", BudgetUSD: budget}); err != nil {
		r.t.Fatal(err)
	}
	return Admission{TaskID: id, ConversationID: conv, Kind: kind, Runtime: "claude", Requested: budget}
}

// usage journals a usage.updated event for the conversation's turn.
func (r *rig) usage(conv, turn, agent string, cost float64) {
	r.t.Helper()
	r.turns++
	if _, err := r.l.DB.Exec(`INSERT OR IGNORE INTO ai_chat_turns (id, conversation_id, profile_id, prompt, created_at, updated_at) VALUES (?,?,'p1','p','x','x')`, turn, conv); err != nil {
		r.t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"agentId":%q,"costUsd":%v,"source":"result"}`, agent, cost)
	if _, err := r.l.DB.Exec(`INSERT INTO ai_chat_events (profile_id, conversation_id, turn_id, seq, created_at, version, type, payload) VALUES ('p1',?,?,?,'x',1,'usage.updated',?)`,
		conv, turn, r.turns, payload); err != nil {
		r.t.Fatal(err)
	}
}

func code(err error) string {
	var ae *aitask.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestResolveIsTheLowestApplicableCapBoundedByTheOperator(t *testing.T) {
	r := newRig(t, 0)
	if c, _ := r.l.Resolve("generic", "c1", ""); c.Set {
		t.Fatalf("nothing set but Resolve says %+v", c)
	}
	for _, s := range []struct {
		scope, key string
		usd        float64
	}{{ScopeGlobal, "", 10}, {ScopeKind, "generic", 5}, {ScopeConversation, "c1", 3}, {ScopeRole, "writer", 4}} {
		if err := r.l.SetCap(s.scope, s.key, s.usd, "operator"); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := r.l.Resolve("generic", "c1", "writer"); c.USD != 3 || c.Source != "conversation:c1" {
		t.Fatalf("lowest of 10/5/3/4 = %+v", c)
	}
	if c, _ := r.l.Resolve("generic", "other", "writer"); c.USD != 4 || c.Source != "role:writer" {
		t.Fatalf("other conversation = %+v", c)
	}
	if c, _ := r.l.Resolve("other-kind", "other", ""); c.USD != 10 {
		t.Fatalf("only the global applies = %+v", c)
	}
	r.l.Operator = 2
	if c, _ := r.l.Resolve("generic", "c1", "writer"); c.USD != 2 || c.Source != "operator" {
		t.Fatalf("the coder budget bounds every cap: %+v", c)
	}
}

// An agent lowers its own conversation's cap and never raises it, whether the
// cap is stored or only inherited.
func TestLowerCapNeverRaises(t *testing.T) {
	r := newRig(t, 0)
	_ = r.l.SetCap(ScopeGlobal, "", 5, "operator")
	if err := r.l.LowerCap(ScopeConversation, "c1", 9, "generic", "agent"); code(err) != CodeCapRaise {
		t.Fatalf("raising above the inherited 5: %v", err)
	}
	if c, _ := r.l.Resolve("generic", "c1", ""); c.USD != 5 {
		t.Fatalf("a refused raise changed the cap: %+v", c)
	}
	if err := r.l.LowerCap(ScopeConversation, "c1", 2, "generic", "agent"); err != nil {
		t.Fatal(err)
	}
	if err := r.l.LowerCap(ScopeConversation, "c1", 4, "generic", "agent"); code(err) != CodeCapRaise {
		t.Fatalf("raising its own 2 to 4: %v", err)
	}
	if c, _ := r.l.Resolve("generic", "c1", ""); c.USD != 2 {
		t.Fatalf("cap = %+v, want 2", c)
	}
	if err := r.l.LowerCap(ScopeConversation, "c1", 0, "generic", "agent"); err != nil {
		t.Fatalf("lowering to 0: %v", err)
	}
	empty := newRig(t, 0)
	if err := empty.l.LowerCap(ScopeConversation, "c1", 1, "generic", "agent"); code(err) != CodeCapRaise {
		t.Fatalf("with no cap anywhere an agent can't set one: %v", err)
	}
}

func TestZeroIsNoSpendAndAMissingCapRefuses(t *testing.T) {
	ctx := context.Background()
	none := newRig(t, 0)
	_, err := none.l.Admit(ctx, none.task("c1", "generic", 1))
	if code(err) != aitask.CodeNoBudget || !strings.Contains(err.Error(), "Settings > AI task budgets") || !strings.Contains(err.Error(), "aitask budget set") {
		t.Fatalf("no cap anywhere: %v", err)
	}
	r := newRig(t, 5)
	_ = r.l.SetCap(ScopeGlobal, "", 0, "operator")
	if _, err := r.l.Admit(ctx, r.task("c1", "generic", 1)); code(err) != aitask.CodeNoBudget {
		t.Fatalf("a global cap of 0 must mean no spend, got %v", err)
	}
	_ = r.l.SetCap(ScopeGlobal, "", 5, "operator")
	_ = r.l.SetCap(ScopeKind, "generic", 0, "operator")
	if _, err := r.l.Admit(ctx, r.task("c2", "generic", 1)); code(err) != aitask.CodeNoBudget {
		t.Fatalf("a kind cap of 0: %v", err)
	}
	if _, err := r.l.Admit(ctx, r.task("c3", "other", 0)); code(err) != aitask.CodeNoBudget {
		t.Fatalf("a request of 0: %v", err)
	}
	if _, err := r.l.Admit(ctx, r.task("c4", "other", 1)); err != nil {
		t.Fatalf("an uncapped kind under a global cap: %v", err)
	}
}

func TestAdmitLowersTheTurnsCapToTheLowestApplicable(t *testing.T) {
	r := newRig(t, 0)
	_ = r.l.SetCap(ScopeGlobal, "", 10, "operator")
	_ = r.l.SetCap(ScopeKind, "generic", 2, "operator")
	a := r.task("c1", "generic", 8)
	got, err := r.l.Admit(context.Background(), a)
	if err != nil || got != 2 {
		t.Fatalf("Admit = %v, %v; want 2", got, err)
	}
	if tk, _ := r.store.Get(a.TaskID); tk.BudgetUSD != 2 {
		t.Fatalf("the task row still carries %v, the turn would run with it", tk.BudgetUSD)
	}
	if again, err := r.l.Admit(context.Background(), a); err != nil || again != 2 {
		t.Fatalf("admitting twice = %v, %v", again, err)
	}
}

// An agent can't dodge a conversation's cap by starting more tasks in it, or
// by spawning workers: both count against the same cap.
func TestAggregateCapHoldsAcrossTasksAndWorkers(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 0)
	_ = r.l.SetCap(ScopeGlobal, "", 100, "operator")
	_ = r.l.SetCap(ScopeConversation, "c1", 5, "operator")

	first := r.task("c1", "generic", 3)
	if _, err := r.l.Admit(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := r.task("c1", "generic", 3)
	if _, err := r.l.Admit(ctx, second); code(err) != CodeExceeded {
		t.Fatalf("3+3 under a cap of 5: %v", err)
	}
	if _, err := r.l.Admit(ctx, r.task("c1", "generic", 2)); err != nil {
		t.Fatalf("3+2 fits: %v", err)
	}
	if _, err := r.l.Admit(ctx, r.task("c2", "generic", 5)); err != nil {
		t.Fatalf("another conversation has its own cap: %v", err)
	}

	// The first task ends having spent 1 itself and 1.5 through a worker.
	r.usage("c1", "turn-1", "", 1)
	r.usage("c1", "turn-1", "w1", 1.5)
	if err := r.l.Settle(ctx, first.TaskID, "c1"); err != nil {
		t.Fatal(err)
	}
	rep, err := r.l.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var used float64
	for _, c := range rep.Caps {
		if c.Scope == ScopeConversation && c.Key == "c1" {
			used = c.UsedUSD
		}
	}
	if used != 2.5+2 { // first: 2.5 spent; third task (still running): 2 held
		t.Fatalf("conversation c1 used %v, want 4.5", used)
	}
	if _, err := r.l.Admit(ctx, r.task("c1", "generic", 1)); code(err) != CodeExceeded {
		t.Fatalf("4.5 used + 1 over 5: %v", err)
	}
	if _, err := r.l.Admit(ctx, r.task("c1", "generic", 0.5)); err != nil {
		t.Fatalf("4.5 used + 0.5 fits: %v", err)
	}
}

func TestGlobalCapHoldsAcrossKindsAndRacingStarts(t *testing.T) {
	r := newRig(t, 0)
	_ = r.l.SetCap(ScopeGlobal, "", 5, "operator")
	var mu sync.Mutex
	admitted := 0
	var wg sync.WaitGroup
	adms := make([]Admission, 12)
	for i := range adms {
		adms[i] = r.task(fmt.Sprintf("c%d", i), fmt.Sprintf("kind%d", i%3), 1)
	}
	for _, a := range adms {
		wg.Add(1)
		go func(a Admission) {
			defer wg.Done()
			if _, err := r.l.Admit(context.Background(), a); err == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}(a)
	}
	wg.Wait()
	if admitted != 5 {
		t.Fatalf("%d of 12 racing 1 USD tasks were admitted under a global cap of 5, want exactly 5", admitted)
	}
}

func TestRuntimeThatCannotEnforceACapIsRefused(t *testing.T) {
	r := newRig(t, 5)
	a := r.task("c1", "generic", 1)
	a.Runtime = "kilo"
	if _, err := r.l.Admit(context.Background(), a); code(err) != CodeUnenforceable {
		t.Fatalf("kilo: %v", err)
	}
}

// A task whose conversation reported no cost keeps its whole reservation: its
// spend is unknown, not zero. One that never ran gives it back.
func TestNoReportedCostKeepsTheReservation(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 0)
	_ = r.l.SetCap(ScopeGlobal, "", 4, "operator")
	ran := r.task("c1", "generic", 3)
	if _, err := r.l.Admit(ctx, ran); err != nil {
		t.Fatal(err)
	}
	// It journaled events, but none with a cost.
	if _, err := r.l.DB.Exec(`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, prompt, created_at, updated_at) VALUES ('t1','c1','p1','p','x','x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.l.DB.Exec(`INSERT INTO ai_chat_events (profile_id, conversation_id, turn_id, seq, created_at, version, type, payload) VALUES ('p1','c1','t1',1,'x',1,'turn.started','{}')`); err != nil {
		t.Fatal(err)
	}
	if err := r.l.Settle(ctx, ran.TaskID, "c1"); err != nil {
		t.Fatal(err)
	}
	// 3 of 4 are still held, so only 1 more fits.
	never := r.task("c2", "generic", 1)
	if _, err := r.l.Admit(ctx, never); err != nil {
		t.Fatalf("the last 1 of 4: %v", err)
	}
	if _, err := r.l.Admit(ctx, r.task("c3", "generic", 1)); code(err) != CodeExceeded {
		t.Fatalf("a task with unknown spend released its cap: %v", err)
	}
	// c2 never ran (nothing journaled): ending it frees its whole reservation.
	if err := r.l.Settle(ctx, never.TaskID, "c2"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.l.Admit(ctx, r.task("c4", "generic", 1)); err != nil {
		t.Fatalf("a task that never ran kept its reservation: %v", err)
	}
}

func TestTaskStatusReportsSpendAndWhetherItWasReported(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 5)
	a := r.task("c1", "generic", 2)
	if st, err := r.l.TaskStatus(ctx, a.TaskID); err != nil || st.Reported || st.CapUSD != 2 {
		t.Fatalf("before any cost: %+v %v", st, err)
	}
	r.usage("c1", "t1", "", 0.4)
	r.usage("c1", "t1", "", 0.9) // a running total: the larger one stands
	r.usage("c1", "t2", "", 0.1) // a second turn adds
	st, err := r.l.TaskStatus(ctx, a.TaskID)
	if err != nil || !st.Reported || st.SpentUSD != 1.0 {
		t.Fatalf("after: %+v %v", st, err)
	}
}

// A follow-up turn is admitted against what is left of the task's cap: a task
// that spent 0.80 of 1 gets a turn capped at 0.20, an exhausted one gets
// none, and a released reservation never makes the spend drop out.
func TestAdmitTurnGivesOnlyWhatIsLeft(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 0)
	_ = r.l.SetCap(ScopeGlobal, "", 10, "operator")
	a := r.task("c1", "generic", 1)
	if _, err := r.l.Admit(ctx, a); err != nil {
		t.Fatal(err)
	}
	r.usage("c1", "turn-1", "", 0.8)
	if err := r.l.Settle(ctx, a.TaskID, "c1"); err != nil { // the first turn ends
		t.Fatal(err)
	}
	got, err := r.l.AdmitTurn(ctx, a, 0)
	if err != nil || got < 0.1999 || got > 0.2001 {
		t.Fatalf("follow-up grant = %v, %v; want 0.20", got, err)
	}
	// While it runs, the task holds 0.80 spent plus its 0.20.
	rep, _ := r.l.Report(ctx)
	if rep.Caps[0].UsedUSD < 0.9999 || rep.Caps[0].UsedUSD > 1.0001 {
		t.Fatalf("global use while the follow-up runs = %v, want 1.00", rep.Caps[0].UsedUSD)
	}
	// A smaller turn cap wins.
	if got, err := r.l.AdmitTurn(ctx, a, 0.05); err != nil || got < 0.0499 || got > 0.0501 {
		t.Fatalf("capped grant = %v, %v", got, err)
	}
	// The follow-up spends all 0.20; the next one is refused with the hint.
	r.usage("c1", "turn-2", "", 0.2)
	if err := r.l.Settle(ctx, a.TaskID, "c1"); err != nil {
		t.Fatal(err)
	}
	_, err = r.l.AdmitTurn(ctx, a, 0)
	if code(err) != aitask.CodeNoBudget || !strings.Contains(err.Error(), "Settings > AI task budgets") {
		t.Fatalf("a follow-up on an exhausted cap: %v", err)
	}
	// A lowered conversation cap bounds the follow-up too.
	b := r.task("c2", "generic", 5)
	if _, err := r.l.Admit(ctx, b); err != nil {
		t.Fatal(err)
	}
	r.usage("c2", "turn-1", "", 1)
	_ = r.l.Settle(ctx, b.TaskID, "c2")
	_ = r.l.SetCap(ScopeConversation, "c2", 1.5, "operator")
	if got, err := r.l.AdmitTurn(ctx, b, 0); err != nil || got < 0.4999 || got > 0.5001 {
		t.Fatalf("grant under a lowered conversation cap = %v, %v; want 0.50", got, err)
	}
}

// A chat lowers its own cap below the one that applies, never to or above it,
// and never from nothing; Admit then runs under the new cap.
func TestLowerConversationCap(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, 0)
	if err := r.l.LowerConversationCap("c1", 1, "agent"); code(err) != CodeNoCapToLower {
		t.Fatalf("no cap applies: %v", err)
	}
	_ = r.l.SetCap(ScopeGlobal, "", 5, "operator")
	for _, usd := range []float64{5, 9} {
		if err := r.l.LowerConversationCap("c1", usd, "agent"); code(err) != CodeCapRaise {
			t.Fatalf("%v at or above the effective 5: %v", usd, err)
		}
	}
	for _, usd := range []float64{math.NaN(), math.Inf(1), -1} {
		if err := r.l.LowerConversationCap("c1", usd, "agent"); err == nil {
			t.Fatalf("%v accepted", usd)
		}
	}
	if _, ok, _ := r.l.get(ScopeConversation, "c1"); ok {
		t.Fatal("a refusal left a cap behind")
	}
	if err := r.l.LowerConversationCap("c1", 2, "agent"); err != nil {
		t.Fatal(err)
	}
	if err := r.l.LowerConversationCap("c1", 2, "agent"); code(err) != CodeCapRaise {
		t.Fatalf("equal to its own 2: %v", err)
	}
	if got, err := r.l.Admit(ctx, r.task("c1", "generic", 4)); err != nil || got != 2 {
		t.Fatalf("Admit = %v, %v; want 2 under the new cap", got, err)
	}
	// The operator's ceiling counts as a cap that applies.
	op := newRig(t, 3)
	if err := op.l.LowerConversationCap("c1", 3, "agent"); code(err) != CodeCapRaise {
		t.Fatalf("at the coder budget: %v", err)
	}
	if err := op.l.LowerConversationCap("c1", 1, "agent"); err != nil {
		t.Fatal(err)
	}
}

// Deleting a conversation cancels its open task, and the room that task held under a cap comes back at the
// next admission: a deleted chat does not keep a reservation (and so a cap's room) forever.
func TestDeletingAConversationFreesWhatItsTaskHeld(t *testing.T) {
	r := newRig(t, 0)
	_ = r.l.SetCap(ScopeGlobal, "", 5, "operator")
	first := r.task("c1", "generic", 5)
	if _, err := r.l.Admit(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := r.task("c2", "generic", 5)
	if _, err := r.l.Admit(context.Background(), second); code(err) != CodeExceeded {
		t.Fatalf("the cap's room is held by the first task, but admitting the second gave %v", err)
	}
	store, err := ai.NewAIStore(r.l.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteConversation("c1", "p1"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.l.Admit(context.Background(), second); err != nil || got != 5 {
		t.Fatalf("after the first conversation was deleted the second task got %v, %v", got, err)
	}
}
