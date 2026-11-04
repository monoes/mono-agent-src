package sched

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/governor/machine"
	"github.com/monoes/mono-agent/internal/doomsday/governor/policy"
	"github.com/monoes/mono-agent/internal/doomsday/governor/queue"
	"github.com/monoes/mono-agent/internal/doomsday/governor/slots"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

var ctx = context.Background()

func TestMain(m *testing.M) { testhome.Main(m) }

// bound fails the test instead of hanging when fn does not return in time.
func bound(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("did not return within %s", d)
	}
}

// fakeClock is a monotonic clock the test moves by hand.
type fakeClock struct{ ns atomic.Int64 }

func (c *fakeClock) Now() time.Duration      { return time.Duration(c.ns.Load()) }
func (c *fakeClock) Advance(d time.Duration) { c.ns.Add(int64(d)) }

// registry answers governor.RunView from the real doomsday store, with a switch to say "not running".
type registry struct {
	st      *store.SQLStore
	notLive atomic.Bool
}

func (r *registry) Running(c context.Context, profile string, gen int64) (bool, error) {
	if r.notLive.Load() {
		return false, nil
	}
	runs, err := r.st.OpenRuns(c, profile)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if run.Profile == profile && run.Generation == gen {
			return run.State == store.StateRunning, nil
		}
	}
	return false, nil
}

func (r *registry) LiveMemberIDs(c context.Context, profile string, gen int64) (map[string]bool, error) {
	ms, err := r.st.LiveMembers(c, profile, gen)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, m := range ms {
		out[m.ID] = true
	}
	return out, nil
}

// env is the real slots and queue over a migrated SQLite database with a running run, plus the fakes.
type env struct {
	db    *sql.DB
	slots *slots.Store
	queue *queue.Queue
	view  *registry
	clock *fakeClock
	pol   *policy.Policy
	smp   *machine.Fake
	l     governor.Limits
}

const (
	capProcess = 4
	capTree    = 2
)

func newEnv(t *testing.T, safeMin int, steps ...machine.Step) *env {
	t.Helper()
	keyring.MockInit()
	path := t.TempDir() + "/dd.db"
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	st := store.New(db.DB, store.NewHMACKeyer([]byte("sched-test-key")))
	g, err := st.NextGeneration(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	c := store.Charter{
		Profile: "p", Generation: g, Goal: "ship it",
		Criteria:    []store.Criterion{{Kind: "command", Spec: "go test ./...", Expected: "ok"}},
		Limits:      store.Limits{TreeDepth: 4, LivePerTree: capTree, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: capProcess, MaxTotal: 100, BudgetMicro: 5_000_000},
		Lines:       []string{store.LineSpending, store.LineInstall},
		BossPin:     store.Pin{Provider: "anthropic", Model: "m1", Version: "v1", PurposePolicy: "judging-v1"},
		VerifierPin: store.Pin{Provider: "anthropic", Model: "m2", Version: "v1", PurposePolicy: "judging-v1"},
		PerAsk:      store.PerAsk{Declared: true, SpendMicro: 1_000_000, Tasks: 5, Access: "edit", Depth: 2, RunSpendMicro: 10_000_000, RunTasks: 50, AnswerClasses: []string{"context", "clarify"}},
	}
	if _, err := st.Enter(ctx, c, c.Hash(), "operator"); err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		steps = []machine.Step{{Sample: machine.Healthy()}}
	}
	view := &registry{st: st}
	clock := &fakeClock{}
	return &env{
		db: db.DB, view: view, clock: clock, pol: policy.New(clock), smp: machine.NewFake(steps...),
		slots: slots.New(db.DB, view), queue: queue.New(db.DB, store.NewHMACKeyer([]byte("sched-test-key"))),
		l: governor.Limits{Profile: "p", Generation: g, MaxConcurrent: capProcess, LivePerTree: capTree, SafeMin: safeMin},
	}
}

// decode reads "provider|member|kind|tree" payloads.
func decode(it governor.Item) (string, governor.Request, error) {
	f := strings.Split(string(it.Payload), "|")
	if len(f) != 4 {
		return "", governor.Request{}, fmt.Errorf("bad payload")
	}
	return f[0], governor.Request{Kind: governor.Kind(f[2]), Tree: f[3], MemberID: f[1]}, nil
}

func payload(provider, member string) []byte {
	return []byte(provider + "|" + member + "|process|")
}

func (e *env) sched(t *testing.T, mut ...func(*Config)) *Scheduler {
	t.Helper()
	cfg := Config{
		Limits: e.l, Sampler: e.smp, Policy: e.pol, Slots: e.slots, Queue: e.queue, View: e.view, Clock: e.clock, Decode: decode,
		SampleEvery: time.Nanosecond, SampleTimeout: 200 * time.Millisecond, CancelTimeout: 300 * time.Millisecond,
	}
	for _, m := range mut {
		m(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *env) enq(t *testing.T, s *Scheduler, lead string, class governor.Class, provider, member string) int64 {
	t.Helper()
	id, err := s.Enqueue(ctx, governor.Item{Lead: lead, Class: class, Payload: payload(provider, member)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) live(t *testing.T) int {
	t.Helper()
	n, err := e.slots.Live(ctx, "p", e.l.Generation, governor.KindProcess, "")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) depth(t *testing.T) int {
	t.Helper()
	n, _, err := e.queue.Depth(ctx, "p", e.l.Generation)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func members(a []Admitted) []string {
	var out []string
	for _, x := range a {
		out = append(out, x.Request.MemberID)
	}
	return out
}

var failSample = machine.Step{Err: fmt.Errorf("no machine")}
