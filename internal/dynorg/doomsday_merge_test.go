package dynorg

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
)

// ddRegistry is the gate's registry for these tests; onStart runs after a step's ticket is recorded, which is
// where a test makes Stop happen at an exact step.
type ddRegistry struct {
	onStart func(t stop.TicketInfo)
}

func (r *ddRegistry) RecordStarted(t stop.TicketInfo, _ string) error {
	if r.onStart != nil {
		r.onStart(t)
	}
	return nil
}
func (r *ddRegistry) RecordAborted(stop.TicketInfo, string) error { return nil }
func (r *ddRegistry) RecordUnknown(stop.TicketInfo) error         { return nil }

// ddMembers wraps the real store so a test can fail one audit kind.
type ddMembers struct {
	*store.SQLStore
	mu       sync.Mutex
	failKind string
}

func (m *ddMembers) AppendAudit(ctx context.Context, p string, g int64, kind, payload string) (store.AuditRow, error) {
	m.mu.Lock()
	fail := m.failKind == kind
	m.mu.Unlock()
	if fail {
		return store.AuditRow{}, errors.New("audit write failed")
	}
	return m.SQLStore.AppendAudit(ctx, p, g, kind, payload)
}

type ddRig struct {
	db   *sql.DB
	st   *ddMembers
	gate *stop.Gate
	reg  *ddRegistry
	run  store.Run
}

func newDDRig(t *testing.T) *ddRig { return newDDRigClock(t, stop.SystemClock{}) }

func newDDRigClock(t *testing.T, clk stop.Clock) *ddRig {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/dd.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	db.DB.SetMaxOpenConns(1)
	st := &ddMembers{SQLStore: store.New(db.DB, store.NewHMACKeyer([]byte("test-vault-derived-key")))}
	c := store.Charter{
		Profile: "p", Generation: 1, Goal: "ship it",
		Criteria: []store.Criterion{{Kind: "command", Spec: "go test ./...", Expected: "ok"}},
		Limits:   store.Limits{TreeDepth: 4, LivePerTree: 30, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: 30, MaxTotal: 100, BudgetMicro: 5_000_000},
		Lines:    []string{store.LineSpending, store.LineInstall},
		BossPin:  store.Pin{Provider: "anthropic", Model: "m1", Version: "v1", PurposePolicy: "judging-v1"}, VerifierPin: store.Pin{Provider: "anthropic", Model: "m2", Version: "v1", PurposePolicy: "judging-v1"},
		PerAsk: store.PerAsk{Declared: true, SpendMicro: 1_000_000, Tasks: 5, Access: "edit", Depth: 2, RunSpendMicro: 10_000_000, RunTasks: 50, AnswerClasses: []string{"context", "clarify"}},
	}
	run, err := st.Enter(context.Background(), c, c.Hash(), "operator")
	if err != nil {
		t.Fatal(err)
	}
	reg := &ddRegistry{}
	return &ddRig{db: db.DB, st: st, reg: reg, run: run, gate: stop.NewGate(stop.GateConfig{Generation: uint64(run.Generation), Reg: reg, Clock: clk})}
}

func (r *ddRig) journal() MergeJournal {
	return SQLMergeJournal{DB: r.db, VerifyAudit: r.st.VerifyAudit}
}

func (r *ddRig) doomsday() *DoomsdayMerge {
	return &DoomsdayMerge{Gate: r.gate, Members: r.st, Journal: r.journal(), Profile: "p", Generation: r.run.Generation,
		Place: func(*exec.Cmd) (func(), error) { return func() {}, nil }}
}

func (r *ddRig) audits(t *testing.T) []string {
	t.Helper()
	var out []string
	rows, err := r.db.Query(`SELECT kind, payload FROM doomsday_audit WHERE profile = 'p' AND generation = ? ORDER BY seq`, r.run.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, p string
		if err := rows.Scan(&k, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, k+" "+p)
	}
	return out
}

func (r *ddRig) has(t *testing.T, kind string) bool {
	for _, a := range r.audits(t) {
		if strings.HasPrefix(a, kind+" ") {
			return true
		}
	}
	return false
}

func (r *ddRig) live(t *testing.T) []store.Member {
	t.Helper()
	ms, err := r.st.LiveMembers(context.Background(), "p", r.run.Generation)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// ddWriter spawns a writer that has written one file, under a conductor whose merges go through the rig.
func ddWriter(t *testing.T, r *ddRig, turn string) (*Conductor, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell hook")
	}
	repo := newRepo(t)
	em := &recEmitter{}
	ex := &fileExec{}
	c := New(context.Background(), Config{
		Cwd: repo, Limits: Limits{MaxAgents: 4, MaxConcurrent: 3},
		Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true,
		Exec: ex.exec, Emit: em, Writers: WritersIsolated, TurnID: turn, Doomsday: r.doomsday(),
	})
	t.Cleanup(c.Close)
	spawnWriter(t, c, "write one.txt x")
	c.Wait(context.Background(), nil, 10*time.Second)
	return c, repo
}

func TestAMergeUnderAnOpenGateRunsFourStepsAndLeavesNoLiveMember(t *testing.T) {
	r := newDDRig(t)
	c, repo := ddWriter(t, r, "d1")
	var steps []string
	r.reg.onStart = func(ti stop.TicketInfo) { steps = append(steps, ti.Step) }
	res, err := c.Merge(context.Background(), "w1")
	if err != nil || !res.Merged {
		t.Fatalf("merge = %+v, %v", res, err)
	}
	if got := strings.Join(steps, ","); got != "checkpoint,pin,merge,record" {
		t.Errorf("steps = %s", got)
	}
	if gitT(t, repo, "log", "-1", "--format=%s") == "" || !r.has(t, "merge.pin") || !r.has(t, "merge.end") {
		t.Errorf("audit = %v", r.audits(t))
	}
	if ms := r.live(t); len(ms) != 0 {
		t.Errorf("member left live: %+v", ms)
	}
	if ops := r.gate.ContinuingTickets(); len(ops) != 0 {
		t.Errorf("operation still continuing: %v", ops)
	}
}

// Q11, second half (T1): a merge that has not begun does not start.
func TestAMergeThatHasNotBegunIsRefusedOnceStopIsInProgress(t *testing.T) {
	r := newDDRig(t)
	c, repo := ddWriter(t, r, "d2")
	branch := "monoagent/d2/w1"
	tipBefore := gitT(t, repo, "rev-parse", "refs/heads/"+branch)
	headBefore := gitT(t, repo, "rev-parse", "HEAD")
	r.gate.Seal()
	_, err := c.Merge(context.Background(), "w1")
	if !errors.Is(err, ErrMergeStopped) {
		t.Fatalf("err = %v, want ErrMergeStopped", err)
	}
	if gitT(t, repo, "rev-parse", "HEAD") != headBefore || gitT(t, repo, "rev-parse", "refs/heads/"+branch) != tipBefore {
		t.Error("the repository or the worker's branch changed")
	}
	if len(r.live(t)) != 0 || r.has(t, "merge.pin") {
		t.Errorf("a refused merge left a trace: %v", r.audits(t))
	}
	if !c.write.tryAcquire() {
		t.Error("the write lease was claimed")
	}
}

// Q11, first half (T3a): Stop lands between the checkpoint and the pin; the begun merge finishes through
// continuation tickets, and Close does not report a barrier before it does.
func TestABegunMergeFinishesAfterStopAndCloseWaitsForIt(t *testing.T) {
	r := newDDRig(t)
	c, repo := ddWriter(t, r, "d3")
	closed := make(chan stop.Outcome, 1)
	r.reg.onStart = func(ti stop.TicketInfo) {
		if ti.Step == "pin" {
			go func() { closed <- r.gate.Close() }()
			for !r.gate.Sealed() {
				time.Sleep(time.Millisecond)
			}
		}
	}
	res, err := c.Merge(context.Background(), "w1")
	if err != nil || !res.Merged {
		t.Fatalf("merge = %+v, %v", res, err)
	}
	if gitT(t, repo, "log", "-1", "--format=%P") == "" {
		t.Error("no merge commit")
	}
	select {
	case o := <-closed:
		if o.Kind != stop.Barrier {
			t.Errorf("outcome = %v, want BARRIER after the merge finished", o.Kind)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close never returned")
	}
	if len(r.live(t)) != 0 {
		t.Error("member left live")
	}
}

// T25: without a durable pin the merge step does not start, and the member is released with the end recorded.
func TestAMergeWithoutADurablePinDoesNotMerge(t *testing.T) {
	r := newDDRig(t)
	c, repo := ddWriter(t, r, "d4")
	head := gitT(t, repo, "rev-parse", "HEAD")
	r.st.failKind = "merge.pin"
	_, err := c.Merge(context.Background(), "w1")
	if err == nil || !strings.Contains(err.Error(), "nothing was merged") {
		t.Fatalf("err = %v", err)
	}
	if gitT(t, repo, "rev-parse", "HEAD") != head {
		t.Error("the repository changed without a pin")
	}
	if len(r.live(t)) != 0 || !r.has(t, "merge.end") {
		t.Errorf("live=%v audit=%v", r.live(t), r.audits(t))
	}
	if ops := r.gate.ContinuingTickets(); len(ops) != 0 {
		t.Errorf("operation still continuing: %v", ops)
	}
}

// T14b: the branch advances between the pin and the merge step; the pinned tip is what gets merged.
func TestTheMergeTakesThePinnedTipNotTheBranchName(t *testing.T) {
	r := newDDRig(t)
	c, repo := ddWriter(t, r, "d5")
	branch := "monoagent/d5/w1"
	tip := gitT(t, repo, "rev-parse", "refs/heads/"+branch)
	var later string
	r.reg.onStart = func(ti stop.TicketInfo) {
		if ti.Step == "merge" {
			later = gitT(t, repo, "commit-tree", "-p", tip, "-m", "late", tip+"^{tree}")
			gitT(t, repo, "update-ref", "refs/heads/"+branch, later)
		}
	}
	if _, err := c.Merge(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if later == "" {
		t.Fatal("hook did not run")
	}
	if _, err := gitRun(context.Background(), repo, "merge-base", "--is-ancestor", tip, "HEAD"); err != nil {
		t.Error("the pinned tip is not in HEAD")
	}
	if _, err := gitRun(context.Background(), repo, "merge-base", "--is-ancestor", later, "HEAD"); err == nil {
		t.Error("a commit newer than the pin was merged")
	}
}

// An operation with no recorded end is not released.
func TestAMergeMemberStaysLiveWhenItsEndCannotBeRecorded(t *testing.T) {
	r := newDDRig(t)
	c, _ := ddWriter(t, r, "d6")
	r.st.failKind = "merge.end"
	if _, err := c.Merge(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if ms := r.live(t); len(ms) != 1 || ms[0].Kind != "merge" {
		t.Fatalf("live = %+v, want the merge member", ms)
	}
}

// Note 13.3 P1: a live merge member (the host died, nothing wrote an end) blocks quiescence and a new run
// whatever any repair state says; a later start revisits it and releases it only on proof.
func TestALiveMergeMemberBlocksQuiescenceUntilALaterStartProvesItGone(t *testing.T) {
	r := newDDRig(t)
	ctx := context.Background()
	if err := r.st.Register(ctx, store.Member{Profile: "p", Generation: r.run.Generation, ID: "merge-dead-1", Kind: "merge", Pid: 4242, Stamp: "host"}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []store.RunState{store.StateStopRequested, store.StateAuthorityRevoked, store.StateCleanupPending} {
		if _, err := r.st.Advance(ctx, "p", r.run.Generation, to, "op", "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.st.Advance(ctx, "p", r.run.Generation, store.StateQuiescent, "op", ""); !errors.Is(err, store.ErrLiveMembers) {
		t.Fatalf("quiescent with a live merge member: %v", err)
	}
	// unknown is not gone: an error and a "not proven" both leave it live
	for _, gone := range []func(store.Member) (bool, error){
		func(store.Member) (bool, error) { return false, nil },
		func(store.Member) (bool, error) { return true, errors.New("cannot tell") },
	} {
		still, err := RevisitMergeMembers(ctx, r.st, "p", r.run.Generation, gone)
		if err != nil || len(still) != 1 {
			t.Fatalf("revisit = %v, %v", still, err)
		}
	}
	if _, err := r.st.Advance(ctx, "p", r.run.Generation, store.StateQuiescent, "op", ""); !errors.Is(err, store.ErrLiveMembers) {
		t.Fatalf("quiescent after an unproven revisit: %v", err)
	}
	still, err := RevisitMergeMembers(ctx, r.st, "p", r.run.Generation, func(store.Member) (bool, error) { return true, nil })
	if err != nil || len(still) != 0 {
		t.Fatalf("revisit = %v, %v", still, err)
	}
	if !r.has(t, "merge.revisit") {
		t.Error("the release left no audit row")
	}
	if _, err := r.st.Advance(ctx, "p", r.run.Generation, store.StateQuiescent, "op", ""); err != nil {
		t.Fatalf("quiescent after proof: %v", err)
	}
}
