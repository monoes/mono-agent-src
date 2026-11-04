package epoch

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// fakeStore mimics sstore's semantics (idempotent insert, forward-only moves) but deliberately does NOT enforce
// "run is running": the engine's own check is what the tests must prove.
type fakeStore struct {
	mu        sync.Mutex
	epochs    map[int64]types.Epoch
	judg      map[int64][]byte
	dirs      map[string]types.Direction
	inserts   int
	moveDirFn func(id string, to types.DirectionState) error // injected failure
	listErr   error
	recorded  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{epochs: map[int64]types.Epoch{}, judg: map[int64][]byte{}, dirs: map[string]types.Direction{}}
}

func (s *fakeStore) addDir(id string, st types.DirectionState) {
	s.dirs[id] = types.Direction{ID: id, Title: "t-" + id, State: st}
}

func (s *fakeStore) sorted() []types.Epoch {
	var out []types.Epoch
	for _, e := range s.epochs {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func (s *fakeStore) LatestEpoch(context.Context, string, int64) (types.Epoch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return types.Epoch{}, false, s.listErr
	}
	all := s.sorted()
	if len(all) == 0 {
		return types.Epoch{}, false, nil
	}
	return all[len(all)-1], true, nil
}

func (s *fakeStore) NextEpochSeq(context.Context, string, int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.epochs)) + 1, nil
}

func (s *fakeStore) ListEpochs(context.Context, string, int64) ([]types.Epoch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sorted(), nil
}

func (s *fakeStore) InsertEpoch(_ context.Context, e types.Epoch) (types.Epoch, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if have, ok := s.epochs[e.Seq]; ok {
		return have, false, nil
	}
	s.epochs[e.Seq] = e
	s.inserts++
	return e, true, nil
}

func (s *fakeStore) RecordJudgment(_ context.Context, _ string, _ int64, seq int64, body []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.epochs[seq]
	if e.State != types.EpochOpen {
		return false, sstore.ErrNeedsOpenEpoch
	}
	e.State, e.HasJudgment, e.JudgmentHash = types.EpochJudged, true, types.BodyHash(body)
	s.epochs[seq], s.judg[seq] = e, body
	s.recorded++
	return true, nil
}

func (s *fakeStore) GetJudgment(_ context.Context, _ string, _ int64, seq int64) (types.JudgmentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.judg[seq]
	if !ok {
		return types.JudgmentRecord{}, sstore.ErrNotFound
	}
	return types.JudgmentRecord{EpochSeq: seq, Body: b, BodyHash: types.BodyHash(b)}, nil
}

func (s *fakeStore) MoveEpoch(_ context.Context, _ string, _ int64, seq int64, to types.EpochState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.epochs[seq]
	if !ok {
		return sstore.ErrNotFound
	}
	if !types.CanMoveEpoch(e.State, to) {
		return sstore.ErrBackwardMove
	}
	e.State = to
	s.epochs[seq] = e
	return nil
}

func (s *fakeStore) OpenDirections(context.Context, string, int64) ([]types.Direction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []types.Direction
	for _, d := range s.dirs {
		if d.State == types.DirProposed || d.State == types.DirActive || d.State == types.DirStalled {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeStore) MoveDirection(_ context.Context, _ string, _ int64, id string, to types.DirectionState, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.moveDirFn != nil {
		if err := s.moveDirFn(id, to); err != nil {
			return err
		}
	}
	d, ok := s.dirs[id]
	if !ok {
		return sstore.ErrNotFound
	}
	if !types.CanMoveDirection(d.State, to) {
		return sstore.ErrBackwardMove
	}
	d.State = to
	s.dirs[id] = d
	return nil
}

func (s *fakeStore) ParkedFinishRounds(context.Context, string, int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.epochs {
		if e.Trigger == types.TriggerFinishParked {
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) state(id string) types.DirectionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dirs[id].State
}

type fakeJudge struct {
	mu     sync.Mutex
	calls  int
	pinErr error
	fn     func(ctx context.Context, in judge.Input) (judge.Result, error)
}

func (j *fakeJudge) CheckPin() (string, error) {
	if j.pinErr != nil {
		return "", j.pinErr
	}
	return "fake/judge/1", nil
}

func (j *fakeJudge) Run(ctx context.Context, in judge.Input) (judge.Result, error) {
	j.mu.Lock()
	j.calls++
	fn := j.fn
	j.mu.Unlock()
	if fn != nil {
		return fn(ctx, in)
	}
	return resultFor(in, nil), nil
}

func (j *fakeJudge) count() int { j.mu.Lock(); defer j.mu.Unlock(); return j.calls }

// resultFor builds a valid judgment for the input: every criterion progressing, verdicts per direction id (default keep).
func resultFor(in judge.Input, verdicts map[string]types.Verdict) judge.Result {
	var jd types.Judgment
	for _, c := range in.Criteria {
		jd.Criteria = append(jd.Criteria, types.CriterionProgress{ID: c.ID, Status: types.CritProgressing})
	}
	for _, d := range in.Directions {
		v := types.VerdictKeep
		if x, ok := verdicts[d.ID]; ok {
			v = x
		}
		jd.Directions = append(jd.Directions, types.DirectionAssessment{ID: d.ID, Verdict: v})
	}
	body, _ := json.Marshal(jd)
	return judge.Result{Judgment: jd, Body: body, BodyHash: types.BodyHash(body), ModelPin: "fake/judge/1"}
}

type fakeInputs struct {
	mu   sync.Mutex
	snap Snapshot
	err  error
}

func (f *fakeInputs) Snapshot(context.Context) (Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, f.err
}

func (f *fakeInputs) set(fn func(*Snapshot)) { f.mu.Lock(); fn(&f.snap); f.mu.Unlock() }

type fakeRun struct {
	mu  sync.Mutex
	on  bool
	err error
}

func (r *fakeRun) Running(context.Context) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.on, r.err
}
func (r *fakeRun) set(v bool) { r.mu.Lock(); r.on = v; r.mu.Unlock() }

type harness struct {
	t    *testing.T
	st   *fakeStore
	jd   *fakeJudge
	in   *fakeInputs
	run  *fakeRun
	clk  *FakeClock
	eng  *Engine
	cfg  Config
	deps Deps
}

func newHarness(t *testing.T, mut func(*Config)) *harness {
	t.Helper()
	h := &harness{t: t, st: newFakeStore(), jd: &fakeJudge{}, in: &fakeInputs{}, run: &fakeRun{on: true},
		clk: NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))}
	h.in.snap = Snapshot{Criteria: []judge.CriterionInput{{ID: "c1", Text: "one"}, {ID: "c2", Text: "two"}}}
	h.cfg = Config{Profile: "p", Generation: 1}
	if mut != nil {
		mut(&h.cfg)
	}
	h.deps = Deps{Store: h.st, Judge: h.jd, Inputs: h.in, Run: h.run, Clock: h.clk}
	h.eng = h.newEngine()
	return h
}

func (h *harness) newEngine() *Engine {
	e, err := New(h.cfg, h.deps)
	if err != nil {
		h.t.Fatal(err)
	}
	return e
}

func (h *harness) tick() TickResult {
	h.t.Helper()
	r, err := h.eng.Tick(context.Background())
	if err != nil {
		h.t.Fatalf("tick: %v", err)
	}
	return r
}

var errBoom = errors.New("boom")

func marshal(j types.Judgment) ([]byte, error) { return json.Marshal(j) }
