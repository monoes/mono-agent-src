package novelty

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

type fakeCorpus struct {
	ledger, dirs map[[32]byte]string
	hits         []Hit
	ledgerErr    error
	dirErr       error
	simErr       error
	calls        int
}

func (f *fakeCorpus) LedgerKey(_ context.Context, k [32]byte) (Match, bool, error) {
	f.calls++
	id, ok := f.ledger[k]
	return Match{ID: id}, ok, f.ledgerErr
}
func (f *fakeCorpus) DirectionKey(_ context.Context, k [32]byte) (Match, bool, error) {
	f.calls++
	id, ok := f.dirs[k]
	return Match{ID: id}, ok, f.dirErr
}
func (f *fakeCorpus) Similar(context.Context, string) ([]Hit, error) {
	f.calls++
	return f.hits, f.simErr
}

func newFake() *fakeCorpus {
	return &fakeCorpus{ledger: map[[32]byte]string{}, dirs: map[[32]byte]string{}}
}

func one(t *testing.T, f *fakeCorpus, title string, unchecked int) (Result, int) {
	t.Helper()
	c, err := New(f, 0.8)
	if err != nil {
		t.Fatal(err)
	}
	r, u := c.Check(context.Background(), []types.Proposal{{Title: title}}, unchecked)
	return r[0], u
}

func TestAcceptedWhenNothingMatches(t *testing.T) {
	f := newFake()
	f.hits = []Hit{{ID: "m1", Score: 0.79}}
	r, u := one(t, f, "build importer", 0)
	if r.Outcome != types.NoveltyAccepted || r.MatchedID != "" || u != 0 {
		t.Fatalf("%+v %d", r, u)
	}
}

func TestRefusedLedgerCitesRow(t *testing.T) {
	f := newFake()
	_, h, _ := Key("build importer")
	f.ledger[h] = "ledger-7"
	r, _ := one(t, f, "Importer: BUILD", 0)
	if r.Outcome != types.NoveltyRefusedLedger || r.MatchedID != "ledger-7" {
		t.Fatalf("%+v", r)
	}
}

func TestRefusedDirectionCitesDirection(t *testing.T) {
	f := newFake()
	_, h, _ := Key("build importer")
	f.dirs[h] = "dir-3"
	r, _ := one(t, f, "build importer", 0)
	if r.Outcome != types.NoveltyRefusedDirection || r.MatchedID != "dir-3" {
		t.Fatalf("%+v", r)
	}
}

func TestLedgerBeforeDirection(t *testing.T) {
	f := newFake()
	_, h, _ := Key("x1")
	f.ledger[h], f.dirs[h] = "L", "D"
	if r, _ := one(t, f, "x1", 0); r.MatchedID != "L" {
		t.Fatalf("%+v", r)
	}
}

func TestThresholdBoundary(t *testing.T) {
	for _, tc := range []struct {
		score float64
		want  types.NoveltyOutcome
	}{
		{0.7999, types.NoveltyAccepted},
		{0.8, types.NoveltyRefusedCorpus}, // at the threshold refuses
		{0.95, types.NoveltyRefusedCorpus},
		{math.NaN(), types.NoveltyRefusedCorpus}, // fail closed
	} {
		f := newFake()
		f.hits = []Hit{{ID: "k9", Score: tc.score, Source: "knowledge"}}
		r, _ := one(t, f, "alpha beta", 0)
		if r.Outcome != tc.want {
			t.Errorf("score %v: %v want %v", tc.score, r.Outcome, tc.want)
		}
		if tc.want == types.NoveltyRefusedCorpus && r.MatchedID != "k9" {
			t.Errorf("score %v cites %q", tc.score, r.MatchedID)
		}
	}
}

func TestUncheckedAllowanceIsOnePerEpoch(t *testing.T) {
	for _, mk := range []func(*fakeCorpus){
		func(f *fakeCorpus) { f.ledgerErr = errors.New("down") },
		func(f *fakeCorpus) { f.dirErr = errors.New("down") },
		func(f *fakeCorpus) { f.simErr = errors.New("down") },
	} {
		f := newFake()
		mk(f)
		c, _ := New(f, 0)
		res, u := c.Check(context.Background(), []types.Proposal{{Title: "one idea"}, {Title: "two idea"}, {Title: "three idea"}}, 0)
		if res[0].Outcome != types.NoveltyAcceptedUnchecked || u != types.MaxUncheckedPerEpoch {
			t.Fatalf("first: %+v %d", res[0], u)
		}
		for _, r := range res[1:] {
			if r.Outcome != types.NoveltyRefusedCorpus || r.MatchedID != "" {
				t.Fatalf("beyond allowance: %+v", r)
			}
		}
		// the counter comes from the caller: an epoch that already used its one refuses the first
		r, u2 := one(t, f, "fresh thing", types.MaxUncheckedPerEpoch)
		if r.Outcome != types.NoveltyRefusedCorpus || u2 != types.MaxUncheckedPerEpoch {
			t.Fatalf("carry-in: %+v %d", r, u2)
		}
	}
}

func TestDefiniteHitBeatsUncheckedAndDoesNotSpendAllowance(t *testing.T) {
	f := newFake()
	f.simErr = errors.New("down")
	_, h, _ := Key("old idea")
	f.ledger[h] = "L1"
	r, u := one(t, f, "old idea", 0)
	if r.Outcome != types.NoveltyRefusedLedger || u != 0 {
		t.Fatalf("%+v %d", r, u)
	}
}

func TestBatchRepeatRefusedAndKeyErrors(t *testing.T) {
	c, _ := New(newFake(), 0)
	res, _ := c.Check(context.Background(), []types.Proposal{
		{Title: "cache layer"}, {Title: "Layer, cache"}, {Title: "the"}, {Title: "retry policy"},
	}, 0)
	if res[0].Outcome != types.NoveltyAccepted {
		t.Fatal(res[0])
	}
	if res[1].Outcome != types.NoveltyRefusedLedger || res[1].MatchedID != "batch:0" {
		t.Fatalf("%+v", res[1])
	}
	if res[2].Err != ErrEmptyKey || res[2].Outcome != "" {
		t.Fatalf("%+v", res[2])
	}
	if res[3].Outcome != types.NoveltyAccepted {
		t.Fatalf("%+v", res[3])
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(nil, 0.5); err == nil {
		t.Error("nil corpus")
	}
	for _, th := range []float64{-1, 1.01, math.NaN(), math.Inf(1)} {
		if _, err := New(newFake(), th); err == nil {
			t.Errorf("threshold %v accepted", th)
		}
	}
	if _, err := New(newFake(), 1); err != nil {
		t.Error(err)
	}
}

// Work bound: a hostile list of any length costs at most MaxBatch normalisations and 3 corpus calls each; the rest is
// refused without a lookup, and a corpus that answers with a huge hit list is read for MaxHits entries only.
func TestWorkBound(t *testing.T) {
	f := newFake()
	c, _ := New(f, 0)
	props := make([]types.Proposal, 200000)
	for i := range props {
		props[i] = types.Proposal{Title: fmt.Sprintf("idea number %d", i)}
	}
	res, _ := c.Check(context.Background(), props, 0)
	if f.calls > 3*MaxBatch {
		t.Fatalf("%d corpus calls", f.calls)
	}
	if res[MaxBatch-1].Outcome != types.NoveltyAccepted || res[MaxBatch].Err != ErrBatchCap || res[len(res)-1].Err != ErrBatchCap {
		t.Fatalf("cap: %+v %+v", res[MaxBatch-1], res[MaxBatch])
	}
}

func TestHitListReadIsBounded(t *testing.T) {
	f := newFake()
	f.hits = make([]Hit, 100000)
	f.hits[MaxHits] = Hit{ID: "late", Score: 1} // past the read bound: not consulted
	if r, _ := one(t, f, "bounded read", 0); r.Outcome != types.NoveltyAccepted {
		t.Fatalf("%+v", r)
	}
	f.hits[MaxHits-1] = Hit{ID: "in", Score: 1}
	if r, _ := one(t, f, "bounded read", 0); r.MatchedID != "in" {
		t.Fatalf("%+v", r)
	}
}

func TestOversizeTitleCostsNothing(t *testing.T) {
	f := newFake()
	c, _ := New(f, 0)
	big := make([]byte, 10<<20)
	for i := range big {
		big[i] = 'a'
	}
	res, _ := c.Check(context.Background(), []types.Proposal{{Title: string(big)}}, 0)
	if res[0].Err != ErrTooLong || f.calls != 0 {
		t.Fatalf("%+v calls=%d", res[0], f.calls)
	}
}
