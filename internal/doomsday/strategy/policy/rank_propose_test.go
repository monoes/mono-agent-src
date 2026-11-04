package policy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/novelty"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

var _ Ledger = (*sstore.Store)(nil)
var _ Gate = (*novelty.Checker)(nil)

type mapScorer map[string]float64

func (m mapScorer) Score(_ context.Context, d types.Direction) (float64, error) {
	v, ok := m[d.ID]
	if !ok {
		return 0, errors.New("no score")
	}
	return v, nil
}

func dir(id string, st types.DirectionState, ep int64) types.Direction {
	return types.Direction{ID: id, State: st, CreatedEpoch: ep}
}

func TestRankOrderAndTieBreaks(t *testing.T) {
	dirs := []types.Direction{dir("b", types.DirActive, 2), dir("a", types.DirActive, 2), dir("c", types.DirProposed, 1),
		dir("d", types.DirPruned, 1), dir("e", types.DirFinished, 1), dir("f", types.DirStalled, 3), dir("g", types.DirActive, 1)}
	sc := mapScorer{"a": 1, "b": 1, "c": 1, "f": 9, "g": math.NaN()}
	r, err := Rank(context.Background(), dirs, sc)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, x := range r {
		ids = append(ids, x.Direction.ID)
	}
	if got := strings.Join(ids, ","); got != "f,c,a,b,g" {
		t.Fatalf("order %s", got)
	}
	if r[len(r)-1].Err != nil && r[len(r)-1].Direction.ID != "g" {
		t.Fatal("wrong last")
	}
	for i := 0; i < 20; i++ { // input order must not matter
		rev := append([]types.Direction(nil), dirs...)
		for a, b := 0, len(rev)-1; a < b; a, b = a+1, b-1 {
			rev[a], rev[b] = rev[b], rev[a]
		}
		r2, _ := Rank(context.Background(), rev, sc)
		for k := range r {
			if r2[k].Direction.ID != r[k].Direction.ID {
				t.Fatalf("not deterministic at %d", k)
			}
		}
	}
}

func TestRankScorerErrorRanksLastAndIsKept(t *testing.T) {
	r, _ := Rank(context.Background(), []types.Direction{dir("x", types.DirActive, 1), dir("y", types.DirActive, 1)}, mapScorer{"y": -5})
	if len(r) != 2 || r[0].Direction.ID != "y" || r[1].Err == nil {
		t.Fatalf("%+v", r)
	}
}

func TestRankBoundsWork(t *testing.T) {
	var ds []types.Direction
	for i := 0; i <= types.MaxDirections; i++ {
		ds = append(ds, dir(fmt.Sprint(i), types.DirActive, 1))
	}
	if _, err := Rank(context.Background(), ds, mapScorer{}); !errors.Is(err, ErrTooManyDirections) {
		t.Fatal(err)
	}
}

func TestPruningOnlyClosesTheDoorToNewWork(t *testing.T) {
	for _, s := range []types.DirectionState{types.DirPruned, types.DirFinished} {
		if AcceptsNewWork(s) {
			t.Fatalf("%s accepts new work", s)
		}
	}
	for _, s := range []types.DirectionState{types.DirProposed, types.DirActive, types.DirStalled} {
		if !AcceptsNewWork(s) {
			t.Fatalf("%s must accept new work", s)
		}
	}
}

// ---- Proposer ----

type fakeCorpus struct {
	ledger map[[32]byte]string
	down   bool  // the memory and knowledge lookup is unreachable
	own    error // the run's own ledger and direction lookups fail
}

func (c *fakeCorpus) LedgerKey(_ context.Context, k [32]byte) (novelty.Match, bool, error) {
	if c.own != nil {
		return novelty.Match{}, false, c.own
	}
	id, ok := c.ledger[k]
	return novelty.Match{ID: id}, ok, nil
}
func (c *fakeCorpus) DirectionKey(context.Context, [32]byte) (novelty.Match, bool, error) {
	if c.own != nil {
		return novelty.Match{}, false, c.own
	}
	return novelty.Match{}, false, nil
}
func (c *fakeCorpus) Similar(context.Context, string) ([]novelty.Hit, error) {
	if c.down {
		return nil, errors.New("down")
	}
	return nil, nil
}

type fakeLedger struct {
	accepted []types.Direction
	notes    []types.NoveltyRow
	refusals []types.NoveltyRow
	acceptEr error
}

func (l *fakeLedger) AcceptProposal(_ context.Context, d types.Direction, n types.NoveltyRow) error {
	if l.acceptEr != nil {
		return l.acceptEr
	}
	l.accepted, l.notes = append(l.accepted, d), append(l.notes, n)
	return nil
}
func (l *fakeLedger) RecordRefusal(_ context.Context, n types.NoveltyRow) error {
	l.refusals = append(l.refusals, n)
	return nil
}

type runState struct {
	ok  bool
	err error
}

func (r runState) Running(context.Context, string, int64) (bool, error) { return r.ok, r.err }

func newProposer(t *testing.T, c *fakeCorpus, l *fakeLedger, rs runState) *Proposer {
	t.Helper()
	chk, err := novelty.New(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	return &Proposer{Profile: "p", Generation: 3, Gate: chk, Ledger: l, Run: rs,
		NewID: func() (string, error) { n++; return fmt.Sprintf("d%d", n), nil }}
}

func prop(title string) types.Proposal {
	return types.Proposal{Title: title, Rationale: "why", ProposerSession: "planner-1"}
}

func TestSubmitAcceptsStoresAndRefuses(t *testing.T) {
	c := &fakeCorpus{ledger: map[[32]byte]string{}}
	_, k, _ := novelty.Key("Known idea")
	c.ledger[k] = "row-7"
	l := &fakeLedger{}
	p := newProposer(t, c, l, runState{ok: true})
	got, unchecked, err := p.Submit(context.Background(), 4, 0, []types.Proposal{prop("Fresh idea"), prop("Known idea"), {Title: ""}, prop("fresh   IDEA")})
	if err != nil || unchecked != 0 {
		t.Fatal(err, unchecked)
	}
	if !got[0].Accepted() || got[0].DirectionID != "d1" {
		t.Fatalf("0: %+v", got[0])
	}
	if got[1].Outcome != types.NoveltyRefusedLedger || got[1].MatchedID != "row-7" || got[1].Accepted() {
		t.Fatalf("1: %+v", got[1])
	}
	if got[2].Err == nil || got[2].Outcome != "" {
		t.Fatalf("2: %+v", got[2])
	}
	if got[3].Outcome != types.NoveltyRefusedLedger || got[3].MatchedID != "batch:0" {
		t.Fatalf("3: %+v", got[3])
	}
	if len(l.accepted) != 1 || l.accepted[0].State != types.DirProposed || l.accepted[0].CreatedEpoch != 4 ||
		l.accepted[0].Profile != "p" || l.accepted[0].Generation != 3 || l.accepted[0].ProposerSession != "planner-1" {
		t.Fatalf("stored %+v", l.accepted)
	}
	if len(l.refusals) != 2 {
		t.Fatalf("refusals %d", len(l.refusals))
	}
}

func TestSubmitNeverStoresAnInvalidProposal(t *testing.T) {
	l := &fakeLedger{}
	p := newProposer(t, &fakeCorpus{}, l, runState{ok: true})
	long := types.Proposal{Title: strings.Repeat("x", types.MaxTitleLen+1), ProposerSession: "s"}
	ctrl := types.Proposal{Title: "bad\x00title", ProposerSession: "s"}
	noProposer := types.Proposal{Title: "fine"}
	tooMany := types.Proposal{Title: "many", ProposerSession: "s", CriterionIDs: make([]string, types.MaxProposalCrit+1)}
	got, _, _ := p.Submit(context.Background(), 1, 0, []types.Proposal{long, ctrl, noProposer, tooMany})
	for i, g := range got {
		if g.Err == nil || g.Accepted() {
			t.Errorf("%d accepted: %+v", i, g)
		}
	}
	if len(l.accepted)+len(l.refusals) != 0 {
		t.Fatal("something was stored")
	}
}

func TestSubmitCorpusDownAllowsOneUncheckedPerEpoch(t *testing.T) {
	l := &fakeLedger{}
	p := newProposer(t, &fakeCorpus{down: true}, l, runState{ok: true})
	got, unchecked, _ := p.Submit(context.Background(), 2, 0, []types.Proposal{prop("one"), prop("two")})
	if got[0].Outcome != types.NoveltyAcceptedUnchecked || !got[0].Accepted() || unchecked != 1 {
		t.Fatalf("%+v %d", got[0], unchecked)
	}
	if got[1].Accepted() || got[1].Outcome != types.NoveltyRefusedCorpus {
		t.Fatalf("second: %+v", got[1])
	}
	got, _, _ = p.Submit(context.Background(), 2, 1, []types.Proposal{prop("three")})
	if got[0].Accepted() {
		t.Fatal("the allowance carries across calls in one epoch")
	}
}

// A tampered or unreadable ledger is not an unreachable corpus: the proposal is not taken in, nothing is stored, and the
// tamper error reaches the caller so it can park the strategy.
func TestSubmitTamperedLedgerIsAHardErrorNotAnUncheckedAcceptance(t *testing.T) {
	l := &fakeLedger{}
	p := newProposer(t, &fakeCorpus{own: sstore.ErrTampered}, l, runState{ok: true})
	got, unchecked, err := p.Submit(context.Background(), 2, 0, []types.Proposal{prop("one")})
	if err != nil || got[0].Accepted() || got[0].Outcome != "" || !errors.Is(got[0].Err, sstore.ErrTampered) || unchecked != 0 {
		t.Fatalf("%+v %d %v", got[0], unchecked, err)
	}
	if len(l.accepted)+len(l.refusals) != 0 {
		t.Fatal("something was stored")
	}
}

func TestSubmitStoreRacesBecomeRefusals(t *testing.T) {
	l := &fakeLedger{acceptEr: &sstore.DuplicateKeyError{ExistingID: "dX"}}
	p := newProposer(t, &fakeCorpus{}, l, runState{ok: true})
	got, _, _ := p.Submit(context.Background(), 1, 0, []types.Proposal{prop("idea")})
	if got[0].Accepted() || got[0].Outcome != types.NoveltyRefusedDirection || got[0].MatchedID != "dX" || len(l.refusals) != 1 {
		t.Fatalf("%+v %d", got[0], len(l.refusals))
	}
	l = &fakeLedger{acceptEr: sstore.ErrUncheckedLimit}
	p = newProposer(t, &fakeCorpus{}, l, runState{ok: true})
	got, _, _ = p.Submit(context.Background(), 1, 0, []types.Proposal{prop("idea")})
	if got[0].Accepted() || got[0].Outcome != types.NoveltyRefusedCorpus {
		t.Fatalf("%+v", got[0])
	}
	l = &fakeLedger{acceptEr: errors.New("disk")}
	p = newProposer(t, &fakeCorpus{}, l, runState{ok: true})
	got, _, _ = p.Submit(context.Background(), 1, 0, []types.Proposal{prop("idea")})
	if got[0].Err == nil || got[0].Accepted() || got[0].Outcome != "" {
		t.Fatalf("%+v", got[0])
	}
}

func TestSubmitInertWhenRunNotRunning(t *testing.T) {
	for _, rs := range []runState{{ok: false}, {err: errors.New("db")}} {
		l := &fakeLedger{}
		p := newProposer(t, &fakeCorpus{}, l, rs)
		_, _, err := p.Submit(context.Background(), 1, 0, []types.Proposal{prop("idea")})
		if !errors.Is(err, ErrRunNotRunning) || len(l.accepted)+len(l.refusals) != 0 {
			t.Fatalf("err=%v stored=%d", err, len(l.accepted))
		}
	}
}

func TestSubmitRefusesAnUnwiredProposer(t *testing.T) {
	if _, _, err := (&Proposer{}).Submit(context.Background(), 1, 0, nil); err == nil {
		t.Fatal("want error")
	}
}

func TestSubmitBatchCapReported(t *testing.T) {
	l := &fakeLedger{}
	p := newProposer(t, &fakeCorpus{}, l, runState{ok: true})
	var ps []types.Proposal
	for i := 0; i < novelty.MaxBatch+3; i++ {
		ps = append(ps, prop(fmt.Sprintf("distinct idea q%d", i)))
	}
	got, _, _ := p.Submit(context.Background(), 1, 0, ps)
	if len(l.accepted) != novelty.MaxBatch || !errors.Is(got[novelty.MaxBatch].Err, novelty.ErrBatchCap) {
		t.Fatalf("accepted %d, last %+v", len(l.accepted), got[novelty.MaxBatch])
	}
}
