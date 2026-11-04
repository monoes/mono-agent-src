package doomsday

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// budgetStore is a ReadViewer whose reads are scripted: each call records the deadline it was given and then either
// returns a verified view or blocks until its context ends.
type budgetStore struct {
	store.Store
	mu        sync.Mutex
	deadlines []time.Time
	script    []func(ctx context.Context) (store.View, error)
}

func (b *budgetStore) ReadView(ctx context.Context, _ string, _ int64, _ store.ViewQuery) (store.View, error) {
	b.mu.Lock()
	dl, _ := ctx.Deadline()
	b.deadlines = append(b.deadlines, dl)
	i := len(b.deadlines) - 1
	b.mu.Unlock()
	return b.script[i](ctx)
}

func withBudget(t *testing.T, d time.Duration) {
	t.Helper()
	old := readTotalBudget
	readTotalBudget = d
	t.Cleanup(func() { readTotalBudget = old })
}

func verifiedView() store.View {
	return store.View{Run: store.Run{Generation: 1}, Chain: store.ChainState{Verified: true, HeadSeq: 5, HeadMAC: []byte{1}}}
}

// errStubHung is what a blocking stub returns when its context never ended: the deadline under test is gone, and the
// test fails after a few seconds instead of holding the runner until the package timeout.
var errStubHung = errors.New("test stub: the context never ended")

func blockUntilDone(ctx context.Context) (store.View, error) {
	select {
	case <-ctx.Done():
		return store.View{}, ctx.Err()
	case <-time.After(5 * time.Second):
		return store.View{}, errStubHung
	}
}

// The shipped budget ends before the desktop binding's 15 s read timeout, with room for process start and printing.
func TestReadTotalBudgetEndsBeforeTheBindingTimeout(t *testing.T) {
	if ReadTotalBudget > 10*time.Second {
		t.Fatalf("ReadTotalBudget %s leaves no room before the 15 s binding timeout", ReadTotalBudget)
	}
}

// Both reads share one deadline: the retry's context ends at the same instant as the first read's.
func TestReadViewRetrySharesTheFirstReadsDeadline(t *testing.T) {
	withBudget(t, 5*time.Second)
	bs := &budgetStore{script: []func(context.Context) (store.View, error){
		func(context.Context) (store.View, error) { return verifiedView(), nil },
		func(context.Context) (store.View, error) { return verifiedView(), nil },
	}}
	e := &StoreEngine{Store: bs, Profile: "p1", HeadAnchor: func(int64, int64, []byte) (bool, string) { return false, store.ReasonBehindWitness }}
	start := time.Now()
	if _, err := e.ReadView(context.Background(), store.ViewQuery{}); err != nil {
		t.Fatal(err)
	}
	if len(bs.deadlines) != 2 {
		t.Fatalf("%d reads, want the read and one retry", len(bs.deadlines))
	}
	if !bs.deadlines[0].Equal(bs.deadlines[1]) || bs.deadlines[0].After(start.Add(5*time.Second+time.Second)) {
		t.Fatalf("deadlines differ or exceed the total budget: %v %v", bs.deadlines[0], bs.deadlines[1])
	}
}

// A read that never finishes ends at the total budget as a read timeout, not as a raw context error.
func TestReadViewTotalBudgetIsAReadTimeout(t *testing.T) {
	withBudget(t, 200*time.Millisecond)
	bs := &budgetStore{script: []func(context.Context) (store.View, error){blockUntilDone}}
	e := &StoreEngine{Store: bs, Profile: "p1"}
	start := time.Now()
	_, err := e.ReadView(context.Background(), store.ViewQuery{})
	if !errors.Is(err, store.ErrViewTimeout) || time.Since(start) > 3*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
}

// The retry may not run past the budget the first read already used: when it runs out the first (red) answer stands.
func TestReadViewRetryOutOfTimeKeepsTheFirstAnswer(t *testing.T) {
	withBudget(t, 300*time.Millisecond)
	bs := &budgetStore{script: []func(context.Context) (store.View, error){
		func(context.Context) (store.View, error) {
			time.Sleep(200 * time.Millisecond)
			return verifiedView(), nil
		},
		blockUntilDone,
	}}
	e := &StoreEngine{Store: bs, Profile: "p1", HeadAnchor: func(int64, int64, []byte) (bool, string) { return false, store.ReasonBehindWitness }}
	start := time.Now()
	v, err := e.ReadView(context.Background(), store.ViewQuery{})
	if err != nil || v.Chain.Verified || v.Chain.Reason != store.ReasonBehindWitness {
		t.Fatalf("view %+v err %v: the first, witness-disagreeing answer must stand", v.Chain, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("the retry ran for %s past the shared budget", time.Since(start))
	}
}

// The caller's own cancel or deadline is not the read's budget running out: it must not read as read_timeout.
func TestReadViewCallerEndingIsNotAReadTimeout(t *testing.T) {
	withBudget(t, 5*time.Second)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	short, cancelShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelShort()
	for name, parent := range map[string]context.Context{"cancelled": cancelled, "caller deadline": short} {
		t.Run(name, func(t *testing.T) {
			bs := &budgetStore{script: []func(context.Context) (store.View, error){blockUntilDone}}
			e := &StoreEngine{Store: bs, Profile: "p1"}
			_, err := e.ReadView(parent, store.ViewQuery{})
			if err == nil || errors.Is(err, store.ErrViewTimeout) || errors.Is(err, errStubHung) {
				t.Fatalf("err %v: a caller that gave up is not read_timeout", err)
			}
		})
	}
}
