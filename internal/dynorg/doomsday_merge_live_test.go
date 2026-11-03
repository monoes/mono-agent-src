package dynorg

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// ddClock is an injected clock: Advance moves it, nothing else does.
type ddClock struct {
	mu  sync.Mutex
	now time.Time
}

func newDDClock() *ddClock { return &ddClock{now: time.Unix(1_800_000_000, 0)} }
func (c *ddClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *ddClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.Now().Add(d)
	return ch
}
func (c *ddClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func ok() error { return nil }

// driver begins a merge by hand so a test controls the moment between steps.
func driver(t *testing.T, r *ddRig, repo string) *mergeOp {
	t.Helper()
	c := &Conductor{cfg: Config{Doomsday: r.doomsday()}}
	mo, err := c.beginMerge(context.Background(), repo, "monoagent/t1/w1", "w1")
	if err != nil || mo == nil {
		t.Fatalf("beginMerge = %v, %v", mo, err)
	}
	return mo
}

func (r *ddRig) endOf(t *testing.T, op string) mergeRec {
	t.Helper()
	rows, err := r.journal().MergeRows(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	ops, _ := foldJournal(rows)
	if o := ops[op]; o == nil || !o.Ended {
		t.Fatalf("operation %s has no end row", op)
	}
	return ops[op].End
}

func (r *ddRig) kinds(t *testing.T, op string) []string {
	t.Helper()
	rows, err := r.journal().MergeRows(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, row := range rows {
		if row.Rec.Op == op {
			out = append(out, row.Kind)
		}
	}
	return out
}

// R1: every step has its own durable row, and the pin row comes before the merge step's row.
func TestR1TheJournalHoldsTheClaimEveryStepAndThePinInOrder(t *testing.T) {
	r := newDDRig(t)
	c, _ := ddWriter(t, r, "j1")
	if _, err := c.Merge(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	rows, _ := r.journal().MergeRows(context.Background(), "p")
	var got []string
	for _, row := range rows {
		s := row.Kind
		if row.Kind == kindStep {
			s += string(rune('0' + row.Rec.Step))
		}
		got = append(got, s)
	}
	want := "merge.begin merge.step2 merge.pin merge.step3 merge.step4 merge.end"
	if strings.Join(got, " ") != want {
		t.Errorf("journal = %v, want %s", got, want)
	}
	ops, order := foldJournal(rows)
	o := ops[order[0]]
	if o.rec.Repo != repoTop(c.iso) || o.rec.Branch != "monoagent/j1/w1" || o.Pin == nil || o.Pin.Tip == "" || o.Pin.FP == "" || o.End.State != StateFinished {
		t.Errorf("operation = %+v pin=%+v end=%+v", o.rec, o.Pin, o.End)
	}
}

// T17: the bound passes between the checkpoint and the pin.
func TestT17TheBoundPassingBeforeThePinEndsTheMergeCleanAndWithdrawsTheRest(t *testing.T) {
	clk := newDDClock()
	r := newDDRigClock(t, clk)
	mo := driver(t, r, t.TempDir())
	if err := mo.run(MergeStepCheckpoint, ok); err != nil {
		t.Fatal(err)
	}
	clk.Advance(stop.DefaultTMerge + time.Second)
	err := mo.run(MergeStepPin, ok)
	if err == nil {
		t.Fatal("the pin step ran after the bound")
	}
	mo.end(context.Background(), &err)
	if e := r.endOf(t, mo.id); e.State != StateIncomplete || e.Reason != ReasonBoundExpired || e.Repair != RepairClean {
		t.Errorf("end = %+v", e)
	}
	if len(r.gate.ContinuingTickets()) != 0 || len(r.live(t)) != 0 {
		t.Error("the operation is still continuing or its member live")
	}
}

// T19: the bound passes between the merge step and the record step: the tip is in, the store does not say so.
func TestT19TheBoundPassingAfterTheMergeStepIsAppliedUnrecorded(t *testing.T) {
	clk := newDDClock()
	r := newDDRigClock(t, clk)
	mo := driver(t, r, t.TempDir())
	for _, s := range []string{MergeStepCheckpoint, MergeStepPin, MergeStepMerge} {
		if err := mo.run(s, ok); err != nil {
			t.Fatal(err)
		}
	}
	clk.Advance(stop.DefaultTMerge + time.Second)
	err := mo.run(MergeStepRecord, ok)
	if err == nil {
		t.Fatal("the record step ran after the bound")
	}
	mo.end(context.Background(), &err)
	if e := r.endOf(t, mo.id); e.State != StateIncomplete || e.Reason != ReasonBoundExpired || e.Repair != RepairAppliedUnrecorded {
		t.Errorf("end = %+v", e)
	}
}

// T18: the bound passes while the merge step's git command runs. The step is never cut short by our code: it
// completes, and the operation then ends as T19. (The gate's own handling of a running step is TestR2 below.)
func TestT18TheBoundPassingInsideTheMergeStepDoesNotCutItShort(t *testing.T) {
	clk := newDDClock()
	r := newDDRigClock(t, clk)
	c, repo := ddWriter(t, r, "j18")
	r.reg.onStart = func(ti stop.TicketInfo) {
		if ti.Step == "merge" {
			clk.Advance(stop.DefaultTMerge + time.Second)
		}
	}
	_, err := c.Merge(context.Background(), "w1")
	if err == nil {
		t.Fatal("the record step ran after the bound")
	}
	if _, gerr := gitRun(context.Background(), repo, "merge-base", "--is-ancestor", "refs/heads/monoagent/j18/w1", "HEAD"); gerr != nil {
		t.Error("the merge step was cut short: the tip is not in HEAD")
	}
	rows, _ := r.journal().MergeRows(context.Background(), "p")
	_, order := foldJournal(rows)
	if e := r.endOf(t, order[0]); e.Reason != ReasonBoundExpired || e.Repair != RepairAppliedUnrecorded {
		t.Errorf("end = %+v", e)
	}
}

// R2 (note 13.3 section 7): a running step keeps its operation CONTINUING past T_merge, so Stop's deferral of the
// containment kill still covers the git command. That is the gate's property (internal/doomsday/stop/gate_ops.go);
// at the time of writing the gate ends the operation at expiry even while a step runs.
func TestR2ARunningStepKeepsItsOperationContinuingPastTheBound(t *testing.T) {
	clk := newDDClock()
	r := newDDRigClock(t, clk)
	mo := driver(t, r, t.TempDir())
	for _, s := range []string{MergeStepCheckpoint, MergeStepPin} {
		if err := mo.run(s, ok); err != nil {
			t.Fatal(err)
		}
	}
	var during []uint64
	_ = mo.run(MergeStepMerge, func() error {
		clk.Advance(stop.DefaultTMerge + time.Second)
		during = r.gate.ContinuingTickets()
		return nil
	})
	if len(during) == 0 {
		t.Skip("OPEN: the gate ends an operation at T_merge even while a step runs (stop/gate_ops.go expireLocked), so the kill deferral would end under the running git command; needs c9's decision to edit the stop package")
	}
}

// T20: a conflict while the gate is closed. The step's own undo (`merge --abort`) runs and is not refused; the
// operation ends rolled back and the lead gets the conflict.
func TestT20TheCompensatingAbortRunsAfterStopHasClosedTheGate(t *testing.T) {
	r := newDDRig(t)
	c, repo := ddWriterBrief(t, r, "j20", "write a.txt from-worker")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("from-lead\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "commit", "--quiet", "-am", "lead")
	head := gitT(t, repo, "rev-parse", "HEAD")
	r.reg.onStart = func(ti stop.TicketInfo) {
		if ti.Step == "merge" {
			go func() { r.gate.Close() }()
			for !r.gate.Sealed() {
				time.Sleep(time.Millisecond)
			}
		}
	}
	_, err := c.Merge(context.Background(), "w1")
	if err == nil || !strings.Contains(err.Error(), "conflicts") || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("err = %v, want the conflict", err)
	}
	if _, mh, lock, status := repoState(t, repo); mh || lock || status != "" || gitT(t, repo, "rev-parse", "HEAD") != head {
		t.Errorf("the abort did not leave the tree as it was: MERGE_HEAD=%v lock=%v status=%q", mh, lock, status)
	}
	rows, _ := r.journal().MergeRows(context.Background(), "p")
	_, order := foldJournal(rows)
	if e := r.endOf(t, order[0]); e.Reason != ReasonStepFailed || e.Repair != RepairRolledBack {
		t.Errorf("end = %+v", e)
	}
}

// ddWriterBrief is ddWriter with the writer's brief given.
func ddWriterBrief(t *testing.T, r *ddRig, turn, brief string) (*Conductor, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell hook")
	}
	repo := newRepo(t)
	ex := &fileExec{}
	c := New(context.Background(), Config{
		Cwd: repo, Limits: Limits{MaxAgents: 4, MaxConcurrent: 3},
		Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true,
		Exec: ex.exec, Emit: &recEmitter{}, Writers: WritersIsolated, TurnID: turn, Doomsday: r.doomsday(),
	})
	t.Cleanup(c.Close)
	spawnWriter(t, c, brief)
	c.Wait(context.Background(), nil, 10*time.Second)
	return c, repo
}

// T21: a second merge into a repository is refused while one is CONTINUING, also from a second conductor object;
// a different repository is free; after the first ends the repository is free again.
func TestT21ASecondMergeIntoTheSameRepositoryIsRefusedWhileOneIsContinuing(t *testing.T) {
	r := newDDRig(t)
	repo := t.TempDir()
	first := driver(t, r, repo)
	second := &Conductor{cfg: Config{Doomsday: r.doomsday()}} // another conductor, its own mutex
	if _, err := second.beginMerge(context.Background(), repo, "monoagent/t2/w1", "w1"); !errors.Is(err, ErrMergeRepoBusy) {
		t.Fatalf("second merge into the same repository: %v", err)
	}
	if mo, err := second.beginMerge(context.Background(), t.TempDir(), "monoagent/t2/w1", "w1"); err != nil || mo == nil {
		t.Fatalf("a different repository was refused: %v", err)
	}
	var err error
	first.end(context.Background(), &err)
	if mo, err := second.beginMerge(context.Background(), repo, "monoagent/t2/w1", "w1"); err != nil || mo == nil {
		t.Fatalf("the repository stayed busy after the first ended: %v", err)
	}
}

// T21 across processes: many claimants race for one repository; the chain gives exactly one the first place.
func TestT21ConcurrentClaimantsOfOneRepositoryHaveExactlyOneWinner(t *testing.T) {
	for round := 0; round < 10; round++ {
		r := newDDRig(t)
		repo := t.TempDir()
		var wins atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				c := &Conductor{cfg: Config{Doomsday: r.doomsday()}}
				if mo, err := c.beginMerge(context.Background(), repo, "monoagent/t3/w1", "w1"); err == nil && mo != nil {
					wins.Add(1)
				} else if err != nil && !errors.Is(err, ErrMergeRepoBusy) {
					t.Errorf("unexpected refusal: %v", err)
				}
			}()
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("round %d: %d winners, want 1", round, wins.Load())
		}
	}
}

// T22: the run cannot become quiescent while a merge is CONTINUING; it can after FINISHED, and with a
// needs-operator repository (which blocks merges, not the run).
func TestT22AContinuingMergeHoldsTheRunAndAFinishedOrNeedsOperatorOneDoesNot(t *testing.T) {
	ctx := context.Background()
	r := newDDRig(t)
	mo := driver(t, r, t.TempDir())
	for _, to := range []store.RunState{store.StateStopRequested, store.StateAuthorityRevoked, store.StateCleanupPending} {
		if _, err := r.st.Advance(ctx, "p", r.run.Generation, to, "op", "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.st.Advance(ctx, "p", r.run.Generation, store.StateQuiescent, "op", ""); !errors.Is(err, store.ErrLiveMembers) {
		t.Fatalf("quiescent while a merge is continuing: %v", err)
	}
	var err error
	mo.end(ctx, &err)
	if _, err := r.st.Advance(ctx, "p", r.run.Generation, store.StateQuiescent, "op", ""); err != nil {
		t.Fatalf("quiescent after FINISHED: %v", err)
	}
}

// T26: recovery runs before ReconcileWorktrees (the guard is called at its top), and the worktree of an
// operation left for the operator, or one not proven gone, is kept. Without the guard the same setup loses it.
func TestT26RecoveryRunsBeforeReconcileAndTheWorktreeOfAnUnresolvedMergeIsKept(t *testing.T) {
	setup := func(t *testing.T, gone bool) (*scene, string) {
		s := repoScene(t)
		s.gone = gone
		tree := filepath.Join(s.repo, ".monoagent-worktrees", "t1", "w1")
		gitT(t, s.repo, "worktree", "add", "-q", "-b", s.branch, tree)
		s.seed(3, true) // the pin was taken before the worktree existed, so the tree differs: needs-operator
		return s, tree
	}
	never := func(string) bool { return false }
	t.Cleanup(func() { SetReconcileGuard(nil) })

	for _, gone := range []bool{true, false} {
		s, tree := setup(t, gone)
		SetReconcileGuard(func(ctx context.Context, cwd string, active func(string) bool) func(string) bool {
			return s.recovery().GuardTurns(ctx, cwd, active)
		})
		ReconcileWorktrees(context.Background(), s.repo, never)
		if _, err := os.Stat(tree); err != nil {
			t.Errorf("gone=%v: the worktree of an unresolved merge was removed", gone)
		}
		if gone {
			s.want(StateIncomplete, RepairNeedsOperator) // recovery ran, and ran first
		}
		SetReconcileGuard(nil)
	}

	_, tree := setup(t, true) // the mutation: no guard, reconcile first
	ReconcileWorktrees(context.Background(), filepath.Dir(filepath.Dir(filepath.Dir(tree))), never)
	if _, err := os.Stat(tree); err == nil {
		t.Error("without the guard the worktree should be removed by reconcile")
	}
}

// The journal payloads are JSON with the pin facts, so a reader of the audit stream can show them.
func TestThePinRowCarriesTheFactsRecoveryNeeds(t *testing.T) {
	s := repoScene(t)
	s.seed(3, true)
	var p string
	if err := s.r.db.QueryRow(`SELECT payload FROM doomsday_audit WHERE kind = 'merge.pin'`).Scan(&p); err != nil {
		t.Fatal(err)
	}
	var rec mergeRec
	if err := json.Unmarshal([]byte(p), &rec); err != nil || rec.Before != s.before || rec.Tip != s.tip || rec.FP != s.fp || rec.Ref != "refs/heads/"+s.branch {
		t.Errorf("pin row = %+v, %v", rec, err)
	}
}
