package governor_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
	"github.com/monoes/mono-agent/internal/doomsday/governor/machine"
	"github.com/monoes/mono-agent/internal/doomsday/governor/policy"
	"github.com/monoes/mono-agent/internal/doomsday/governor/queue"
	"github.com/monoes/mono-agent/internal/doomsday/governor/sched"
	"github.com/monoes/mono-agent/internal/doomsday/governor/slots"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
)

// The whole governor over a real SQLite database with a real doomsday run: only the machine (a scripted sampler), the
// clock and the processes (the test releases slots itself) are fakes.

type clock struct{ ns atomic.Int64 }

func (c *clock) Now() time.Duration { return time.Duration(c.ns.Load()) }

type runView struct {
	st *store.SQLStore
	mu sync.Mutex
	up map[string]bool
}

func (r *runView) Running(c context.Context, p string, g int64) (bool, error) {
	runs, err := r.st.OpenRuns(c, p)
	if err != nil {
		return false, err
	}
	for _, x := range runs {
		if x.Generation == g {
			return x.State == store.StateRunning, nil
		}
	}
	return false, nil
}

func (r *runView) LiveMemberIDs(context.Context, string, int64) (map[string]bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]bool{}
	for k, v := range r.up {
		out[k] = v
	}
	return out, nil
}

type world struct {
	db   *sql.DB
	st   *store.SQLStore
	view *runView
	slot *slots.Store
	q    *queue.Queue
	smp  *machine.Fake
	s    *sched.Scheduler
	l    governor.Limits
}

func decode(it governor.Item) (string, governor.Request, error) {
	return "", governor.Request{Kind: governor.KindProcess, MemberID: string(it.Payload)}, nil
}

func newWorld(t *testing.T, cap int, queueRows int, steps ...machine.Step) *world {
	return newWorldBytes(t, cap, queueRows, 0, steps...)
}

func newWorldBytes(t *testing.T, cap int, queueRows int, queueBytes int64, steps ...machine.Step) *world {
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
	keys := store.NewHMACKeyer([]byte("e2e-key"))
	st := store.New(db.DB, keys)
	c := store.Charter{
		Profile: "p", Generation: 1, Goal: "ship it",
		Criteria:    []store.Criterion{{Kind: "command", Spec: "go test ./...", Expected: "ok"}},
		Limits:      store.Limits{TreeDepth: 4, LivePerTree: cap, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: cap, MaxTotal: 1000, BudgetMicro: 5_000_000, QueueMaxRows: queueRows, QueueMaxBytes: queueBytes},
		Lines:       []string{store.LineSpending},
		BossPin:     store.Pin{Provider: "a", Model: "m1", Version: "v1", PurposePolicy: "j"},
		VerifierPin: store.Pin{Provider: "a", Model: "m2", Version: "v1", PurposePolicy: "j"},
	}
	if _, err := st.Enter(context.Background(), c, c.Hash(), "operator"); err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		steps = []machine.Step{{Sample: machine.Healthy()}}
	}
	view := &runView{st: st, up: map[string]bool{}}
	w := &world{db: db.DB, st: st, view: view, smp: machine.NewFake(steps...), slot: slots.New(db.DB, view), q: queue.New(db.DB, keys),
		l: governor.Limits{Profile: "p", Generation: 1, MaxConcurrent: cap, LivePerTree: cap, SafeMin: 1, QueueMaxRows: queueRows, QueueMaxBytes: queueBytes}}
	clk := &clock{}
	w.s, err = sched.New(sched.Config{Limits: w.l, Sampler: w.smp, Policy: policy.New(clk), Slots: w.slot, Queue: w.q, View: view, Clock: clk, Decode: decode,
		SampleEvery: time.Nanosecond, SampleTimeout: 200 * time.Millisecond, CancelTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) live(t *testing.T) int {
	t.Helper()
	n, err := w.slot.Live(context.Background(), "p", 1, governor.KindProcess, "")
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// Width is a queue, not a number of processes: many launches from several leads run through a cap of 3, every launch runs
// exactly once, the live count never passes the cap, a hostile lead cannot keep another from its first slot, and a machine that
// goes bad lowers what is admitted without touching what runs.
func TestWidthIsAQueueAndTheCapNeverBreaks(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 3, 500, machine.Step{Sample: machine.Healthy()}, machine.Step{Sample: machine.Healthy()}, machine.Step{Err: errors.New("proc unreadable")})
	enq := func(lead, id string) {
		if _, err := w.s.Enqueue(ctx, governor.Item{Lead: lead, Class: governor.ClassNormal, Payload: []byte(id)}); err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
	}
	for i := 0; i < 120; i++ { // the hostile lead's width
		enq("hostile", fmt.Sprintf("h%03d", i))
	}
	enq("quiet", "q000")
	enq("quiet2", "r000")

	ran := map[string]int{}
	running := map[string]bool{}
	first := ""
	for round := 0; round < 400 && len(ran) < 122; round++ {
		res, err := w.s.Pump(ctx)
		if err != nil {
			t.Fatalf("pump: %v", err)
		}
		for _, a := range res.Admitted {
			id := a.Request.MemberID
			ran[id]++
			running[id] = true
			w.view.mu.Lock()
			w.view.up[id] = true
			w.view.mu.Unlock()
			if first == "" {
				first = id
			}
		}
		if n := w.live(t); n > 3 {
			t.Fatalf("round %d: %d live slots under a cap of 3", round, n)
		}
		if round == 0 && !(ran["q000"] == 1 && ran["r000"] == 1) {
			t.Fatalf("the quiet leads did not get their first slot in the first round: %v", ran)
		}
		for id := range running { // one finishes per round
			if err := w.s.Release(ctx, id); err != nil {
				t.Fatal(err)
			}
			delete(running, id)
			break
		}
	}
	if len(ran) != 122 {
		t.Fatalf("%d of 122 launches ran", len(ran))
	}
	for id, n := range ran {
		if n != 1 {
			t.Fatalf("%s ran %d times", id, n)
		}
	}
	if rows, _, _ := w.q.Depth(ctx, "p", 1); rows != 0 {
		t.Fatalf("%d launches still queued", rows)
	}
}

// A queue that is full is a visible refusal and writes nothing; the byte bound is the payload's own size.
func TestQueueBoundIsEnforcedAndARefusalWritesNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 2, 3)
	for i := 0; i < 3; i++ {
		if _, err := w.s.Enqueue(ctx, governor.Item{Lead: "l", Class: governor.ClassNormal, Payload: []byte(fmt.Sprint("m", i))}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ {
		if _, err := w.s.Enqueue(ctx, governor.Item{Lead: "l", Class: governor.ClassNormal, Payload: []byte("flood")}); !errors.Is(err, governor.ErrQueueFull) {
			t.Fatalf("flood %d: %v", i, err)
		}
	}
	var rows int
	if err := w.db.QueryRow(`SELECT COUNT(*) FROM doomsday_queue`).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("%d rows after 50 refusals (%v), want 3: a refusal must write nothing", rows, err)
	}
	// a payload larger than the byte bound never reaches the table
	big := strings.Repeat("x", governor.DefaultQueueMaxBytes+1)
	w2 := newWorld(t, 2, 0)
	if _, err := w2.s.Enqueue(ctx, governor.Item{Lead: "l", Class: governor.ClassNormal, Payload: []byte(big)}); !errors.Is(err, governor.ErrQueueFull) {
		t.Fatalf("an oversized payload: %v", err)
	}
	var bytes int64
	_ = w2.db.QueryRow(`SELECT COALESCE(SUM(length(payload)),0) FROM doomsday_queue`).Scan(&bytes)
	if bytes != 0 {
		t.Fatalf("%d payload bytes stored after the refusal", bytes)
	}
}

// Stop wins: a Stop in the middle of a pump run cancels the queue, nothing is admitted afterwards, and the running members
// keep their slots (the governor kills nothing; Stop's own order does).
func TestStopCancelsTheQueueAndAdmitsNothingAfterIt(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 2, 50)
	for i := 0; i < 10; i++ {
		if _, err := w.s.Enqueue(ctx, governor.Item{Lead: "l", Class: governor.ClassNormal, Payload: []byte(fmt.Sprint("m", i))}); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := w.s.Pump(ctx); err != nil || len(res.Admitted) != 2 {
		t.Fatalf("pump: %v %v", res, err)
	}
	n, err := w.s.Stop(ctx)
	if err != nil || n != 8 {
		t.Fatalf("stop cancelled %d (%v), want the 8 waiting", n, err)
	}
	if _, err := w.s.Pump(ctx); err == nil {
		t.Fatal("a pump after Stop admitted work")
	}
	if err := w.s.TryAdmit(ctx, "", governor.Request{Kind: governor.KindProcess, MemberID: "late"}); err == nil {
		t.Fatal("TryAdmit after Stop admitted")
	}
	if w.live(t) != 2 {
		t.Fatalf("%d live slots: Stop's governor must not release running members", w.live(t))
	}
	if rows, _, _ := w.q.Depth(ctx, "p", 1); rows != 0 {
		t.Fatalf("%d rows still queued after Stop", rows)
	}
}

// Racing for the last slot admits exactly one, and a leak (a member that vanished) is reconciled, a live one is not.
func TestLastSlotRaceAndLeakReconcile(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 3, 50)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprint("r", i)
			w.view.mu.Lock()
			w.view.up[id] = true
			w.view.mu.Unlock()
			if w.s.TryAdmit(ctx, "", governor.Request{Kind: governor.KindProcess, MemberID: id}) == nil {
				admitted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if admitted.Load() != 3 || w.live(t) != 3 {
		t.Fatalf("%d admitted, %d live, want exactly the cap of 3", admitted.Load(), w.live(t))
	}
	// two members vanish without a release (a crashed host); one stays alive
	w.view.mu.Lock()
	kept := ""
	for id := range w.view.up {
		if w.slotHeld(t, id) && kept == "" {
			kept = id
			continue
		}
		delete(w.view.up, id)
	}
	w.view.mu.Unlock()
	freed, err := w.slot.Reconcile(ctx, "p", 1)
	if err != nil || freed != 2 || w.live(t) != 1 {
		t.Fatalf("reconcile freed %d (%v), %d live, want 2 freed and the live member kept", freed, err, w.live(t))
	}
}

func (w *world) slotHeld(t *testing.T, id string) bool {
	var n int
	if err := w.db.QueryRow(`SELECT COUNT(*) FROM doomsday_slots WHERE member_id = ? AND released_at IS NULL`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// A machine that cannot be read lowers admission to the safe minimum and never to the cap.
func TestAnUnreadableMachineAdmitsOnlyTheSafeMinimum(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t, 6, 50, machine.Step{Err: errors.New("proc unreadable")})
	got := 0
	for i := 0; i < 6; i++ {
		if w.s.TryAdmit(ctx, "", governor.Request{Kind: governor.KindProcess, MemberID: fmt.Sprint("m", i)}) == nil {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("%d admitted with an unreadable machine and a safe minimum of 1", got)
	}
	if e := w.s.Effective(ctx); e.Value != 1 || e.Cap != 6 {
		t.Fatalf("effective %+v", e)
	}
}

// The byte bound is cumulative and counts the payload's own size: many small items cannot add up past it, and the refusals
// store nothing (a work bound, not only a refusal message).
func TestQueueByteBoundIsCumulativeAndAllocatesNothingPastIt(t *testing.T) {
	ctx := context.Background()
	w := newWorldBytes(t, 2, 500, 100)
	payload := []byte(strings.Repeat("p", 40))
	accepted := 0
	for i := 0; i < 20; i++ {
		if _, err := w.s.Enqueue(ctx, governor.Item{Lead: "l", Class: governor.ClassNormal, Payload: payload}); err == nil {
			accepted++
		} else if !errors.Is(err, governor.ErrQueueFull) {
			t.Fatal(err)
		}
	}
	var rows int
	var bytes int64
	if err := w.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(length(payload)),0) FROM doomsday_queue`).Scan(&rows, &bytes); err != nil {
		t.Fatal(err)
	}
	if accepted != 2 || rows != 2 || bytes != 80 {
		t.Fatalf("accepted %d, stored %d rows and %d payload bytes under a bound of 100 with 40-byte items", accepted, rows, bytes)
	}
}
