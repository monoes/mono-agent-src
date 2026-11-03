//go:build doomsday

package doomsday

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/policy"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// StrategyCriterion is one charter criterion as the judge sees it. Text is the charter's own words.
type StrategyCriterion struct {
	ID   string
	Text string
}

// StrategyRun is what the host knows of the open run of its profile.
type StrategyRun struct {
	Generation int64
	CharterID  string
	Hash       string
	Running    bool // state is exactly "running"; every other open state is a run on its way out
	Boss       int64
	HasBoss    bool
	Criteria   []StrategyCriterion
	// Judge is the pin the judging purpose resolves to for this run (the charter's boss pin: the judge may equal the boss,
	// Q2), Verifier the finish verifier's. The independence rule keeps the judge from being the verifier.
	Judge, Verifier StrategyPin
}

// StrategyPin names a model as the charter pinned it.
type StrategyPin struct{ Provider, Model, Version string }

// String is the pin as the epoch row records it.
func (p StrategyPin) String() string {
	if p.Provider == "" || p.Model == "" {
		return ""
	}
	return p.Provider + "/" + p.Model + "@" + p.Version
}

// CriterionIDs are the ids the judge must score.
func (r StrategyRun) CriterionIDs() []string {
	ids := make([]string, len(r.Criteria))
	for i, c := range r.Criteria {
		ids[i] = c.ID
	}
	return ids
}

// StrategyRunSource finds the open run. ok is false when the profile has none.
type StrategyRunSource interface {
	Current(ctx context.Context) (run StrategyRun, ok bool, err error)
}

// StoreRunSource is the StrategyRunSource over the store package and the boss registry.
type StoreRunSource struct {
	Profile string
	Runs    interface {
		OpenRuns(ctx context.Context, profile string) ([]store.Run, error)
		LoadCharter(ctx context.Context, profile string, generation int64) (store.Charter, error)
	}
	Bosses ops.Bosses
}

// Current prefers the running generation; otherwise the newest open one. A boss that cannot be read is no boss, so the
// finish policy waits.
func (s StoreRunSource) Current(ctx context.Context) (StrategyRun, bool, error) {
	runs, err := s.Runs.OpenRuns(ctx, s.Profile)
	if err != nil || len(runs) == 0 {
		return StrategyRun{}, false, err
	}
	pick := runs[len(runs)-1]
	for _, r := range runs {
		if r.State == store.StateRunning {
			pick = r
		}
	}
	c, err := s.Runs.LoadCharter(ctx, s.Profile, pick.Generation)
	if err != nil {
		return StrategyRun{}, false, err
	}
	h := c.Hash()
	out := StrategyRun{Generation: pick.Generation, CharterID: c.ID(), Hash: hex.EncodeToString(h[:]), Running: pick.State == store.StateRunning,
		Judge:    StrategyPin{c.BossPin.Provider, c.BossPin.Model, c.BossPin.Version},
		Verifier: StrategyPin{c.VerifierPin.Provider, c.VerifierPin.Model, c.VerifierPin.Version}}
	for i, cr := range c.Criteria {
		out.Criteria = append(out.Criteria, StrategyCriterion{ID: store.CriterionID(i), Text: cr.Spec})
	}
	if s.Bosses != nil && out.Running {
		act := ops.Active{ID: out.CharterID, Hash: out.Hash, Generation: out.Generation, Profile: s.Profile}
		if id, ok, berr := s.Bosses.BossOf(ctx, act); berr == nil && ok {
			out.Boss, out.HasBoss = id, true
		}
	}
	return out, true, nil
}

// StrategyHostDeps are the collaborators of the host. Required: DB, Keys, Audit, Seq, Profile, Runs, Model (or ModelFor),
// Refuse, Clock.
type StrategyHostDeps struct {
	DB    *sql.DB
	Keys  sstore.Keyer
	Audit ChainTxAppender
	// Seq runs every write of the layer as a witnessed transition (the production one is WitnessSequencer over the
	// profile's stop.Sequencer). It is required: a chained row written outside the witness makes the next admission of
	// the generation fail closed, so a host without one does not exist.
	Seq     sstore.Sequencer
	Profile string
	Runs    StrategyRunSource

	Model judge.Model
	// ModelFor, when set, is asked for the judge model of each generation it syncs to (the pin of that run's charter);
	// it wins over Model.
	ModelFor func(run StrategyRun) judge.Model
	Refuse   func(pin string) bool // the independence rule of the judge; see judge.Config
	// RefuseFor, when set, is the independence rule of each generation it syncs to; it wins over Refuse.
	RefuseFor func(run StrategyRun) func(pin string) bool
	Timeout   time.Duration
	Clock     epoch.Clock

	Epoch  epoch.Config  // Profile and Generation are set by the host
	Policy policy.Config // MaxJudgmentAge

	// Finish is FinishEngine.FinishBegin. Nil means strategy never requests a finish.
	Finish func(ctx context.Context) (BeginReport, error)
	// SubjectChangedAt is the last change of the verified subject (a new freeze or a new subject digest). Nil means it
	// cannot be told, and then no finish is ever requested (an unknown time is read as stale, which fails closed).
	SubjectChangedAt func(ctx context.Context, generation int64) (time.Time, error)

	// Optional sources. Nil means the corresponding trigger never fires.
	Activity func(ctx context.Context, open []types.Direction) ([]epoch.DirectionActivity, error)
	Evidence func(ctx context.Context) (int64, error)
	Similar  SimilarFunc

	NoveltyThreshold float64
	NewID            func() (string, error) // nil: random
}

// StrategyHost is the host loop object of the strategy layer. It has no goroutine: the engine's loop calls Tick, and the
// ops layer calls through Sink. The zero value is not usable.
type StrategyHost struct {
	d     StrategyHostDeps
	store *sstore.Store
	runs  runStateDB

	opsOnly bool // built by NewStrategyOpsHost: it takes operations and never ticks

	tickMu sync.Mutex
	gen    *genState // owned by Tick
}

// genState is the in-memory state of one generation. A restart rebuilds it from the database (see syncGen).
type genState struct {
	gen          int64
	engine       *epoch.Engine
	parks        int
	lastParkedAt time.Time
	gaveUp       bool
	finishing    bool
	parked       bool
}

// NewStrategyHost validates the dependencies and builds the host. It reads and writes nothing.
func NewStrategyHost(d StrategyHostDeps) (*StrategyHost, error) {
	if (d.Model == nil && d.ModelFor == nil) || (d.Refuse == nil && d.RefuseFor == nil) || d.Clock == nil {
		return nil, errors.New("strategy host: Model, Refuse and Clock are required")
	}
	return newStrategyHost(d)
}

// NewStrategyOpsHost is the host of a process that only takes the session-callable operations (the MCP server): it has
// the sink and no judge, so it cannot Tick. It needs the same store, witness and run source as the ticking host.
func NewStrategyOpsHost(d StrategyHostDeps) (*StrategyHost, error) {
	h, err := newStrategyHost(d)
	if err != nil {
		return nil, err
	}
	h.opsOnly = true
	return h, nil
}

func newStrategyHost(d StrategyHostDeps) (*StrategyHost, error) {
	if d.DB == nil || d.Keys == nil || d.Audit == nil || d.Seq == nil || d.Profile == "" || d.Runs == nil {
		return nil, errors.New("strategy host: DB, Keys, Audit, Seq, Profile and Runs are required")
	}
	if d.NewID == nil {
		d.NewID = randomID
	}
	db := sstore.WrapDB(d.DB)
	return &StrategyHost{d: d, store: sstore.New(db, d.Keys, NewStrategyAuditSink(d.Audit)).WithSequencer(d.Seq),
		runs: runStateDB{db: d.DB, profile: d.Profile}}, nil
}

// policySink is the policy.Sink of one generation: a strategy line in its own witnessed transition.
func (h *StrategyHost) policySink(gen int64) policySink {
	return policySink{db: sstore.WrapDB(h.d.DB), app: h.d.Audit, seq: h.d.Seq, profile: h.d.Profile, gen: gen}
}

// Sink is the ops.StrategySink the typed strategy operations write through.
func (h *StrategyHost) Sink() ops.StrategySink { return strategySink{h: h} }

// HostTick is what one Tick did. It carries ids, enums and counts, never model text.
type HostTick struct {
	Generation int64
	Idle       string // "no-run" or "parked" when nothing was done
	Epoch      *epoch.TickResult
	Decision   *policy.Decision
	Finish     *policy.Outcome // the finish engine's answer, when a finish was requested
}

// Tick advances the strategy layer by at most one epoch step and one finish-policy step. With no open run it does
// nothing. After a Stop it only lets the engine close an unfinished epoch as abandoned; no finish is ever requested for
// a run that is not running. A stored row that fails its MAC parks the strategy for that generation (not the run).
func (h *StrategyHost) Tick(ctx context.Context) (HostTick, error) {
	if h.opsOnly {
		return HostTick{Idle: "no-run"}, errors.New("strategy host: this host takes operations only")
	}
	h.tickMu.Lock()
	defer h.tickMu.Unlock()
	run, ok, err := h.d.Runs.Current(ctx)
	if err != nil {
		return HostTick{Idle: "no-run"}, err
	}
	if !ok {
		h.gen = nil
		return HostTick{Idle: "no-run"}, nil
	}
	gs, err := h.syncGen(ctx, run)
	if err != nil {
		return HostTick{Generation: run.Generation}, h.noteTamper(ctx, nil, run, err)
	}
	out := HostTick{Generation: run.Generation}
	if gs.parked {
		out.Idle = "parked"
		return out, nil
	}
	res, terr := gs.engine.Tick(ctx)
	out.Epoch = &res
	if terr != nil {
		if errors.Is(terr, sstore.ErrTampered) {
			return out, h.noteTamper(ctx, gs, run, terr)
		}
		return out, terr
	}
	if res.Outcome == epoch.OutcomeInert || !run.Running {
		return out, nil
	}
	dec, fo, serr := h.finishStep(ctx, gs, run)
	out.Decision, out.Finish = dec, fo
	if serr != nil && errors.Is(serr, sstore.ErrTampered) {
		return out, h.noteTamper(ctx, gs, run, serr)
	}
	return out, serr
}

// noteTamper parks the strategy for the generation and records it. The run is not touched.
func (h *StrategyHost) noteTamper(ctx context.Context, gs *genState, run StrategyRun, err error) error {
	if !errors.Is(err, sstore.ErrTampered) {
		return err
	}
	if gs != nil {
		gs.parked = true
	} else {
		h.gen = &genState{gen: run.Generation, parked: true}
	}
	_ = h.policySink(run.Generation).Record(ctx, types.KindParked, "a stored strategy row failed verification")
	return err
}

// syncGen keeps the per-generation state, rebuilding it when the generation changes or after a restart.
func (h *StrategyHost) syncGen(ctx context.Context, run StrategyRun) (*genState, error) {
	if h.gen != nil && h.gen.gen == run.Generation {
		return h.gen, nil
	}
	gs := &genState{gen: run.Generation}
	eps, err := h.store.ListEpochs(ctx, h.d.Profile, run.Generation)
	if err != nil {
		return nil, err
	}
	for _, e := range eps {
		if e.Trigger == types.TriggerFinishParked {
			gs.parks++
			gs.lastParkedAt = time.Unix(e.StartedAt, 0) // the park preceded the epoch it triggered
		}
	}
	cfg := h.d.Epoch
	cfg.Profile, cfg.Generation = h.d.Profile, run.Generation
	model := h.d.Model
	if h.d.ModelFor != nil {
		model = h.d.ModelFor(run)
	}
	refuse := h.d.Refuse
	if h.d.RefuseFor != nil {
		refuse = h.d.RefuseFor(run)
	}
	jd, err := judge.New(judge.Config{Model: model, Refuse: refuse, Timeout: h.d.Timeout})
	if err != nil {
		return nil, err
	}
	eng, err := epoch.New(cfg, epoch.Deps{Store: h.store, Judge: jd, Inputs: hostInputs{h: h, gs: gs},
		Run: genRun{r: h.runs, gen: run.Generation}, Clock: h.d.Clock})
	if err != nil {
		return nil, err
	}
	gs.engine = eng
	h.gen = gs
	return gs, nil
}

// hostInputs is epoch.Inputs: the charter's criteria with the previous reading, the activity and evidence sources.
type hostInputs struct {
	h  *StrategyHost
	gs *genState
}

func (in hostInputs) Snapshot(ctx context.Context) (epoch.Snapshot, error) {
	h := in.h
	run, ok, err := h.d.Runs.Current(ctx)
	if err != nil || !ok || run.Generation != in.gs.gen {
		return epoch.Snapshot{}, errors.Join(errors.New("strategy host: the run changed"), err)
	}
	last := map[string]types.CriterionStatus{}
	if j, _, found, jerr := h.latestJudgment(ctx, run.Generation); jerr != nil {
		return epoch.Snapshot{}, jerr
	} else if found {
		for _, c := range j.Criteria {
			last[c.ID] = c.Status
		}
	}
	snap := epoch.Snapshot{FinishParks: in.gs.parks}
	for _, c := range run.Criteria {
		snap.Criteria = append(snap.Criteria, judge.CriterionInput{ID: c.ID, Text: c.Text, LastStatus: last[c.ID]})
	}
	if h.d.Activity != nil {
		open, oerr := h.store.OpenDirections(ctx, h.d.Profile, run.Generation)
		if oerr != nil {
			return epoch.Snapshot{}, oerr
		}
		if snap.Activity, err = h.d.Activity(ctx, open); err != nil {
			return epoch.Snapshot{}, fmt.Errorf("strategy host: activity: %w", err)
		}
	}
	if h.d.Evidence != nil {
		if snap.EvidenceSeq, err = h.d.Evidence(ctx); err != nil {
			return epoch.Snapshot{}, fmt.Errorf("strategy host: evidence: %w", err)
		}
	}
	return snap, nil
}

// latestJudgment is the newest judgment that is not a bad one, with its epoch.
func (h *StrategyHost) latestJudgment(ctx context.Context, gen int64) (types.Judgment, types.Epoch, bool, error) {
	eps, err := h.store.ListEpochs(ctx, h.d.Profile, gen)
	if err != nil {
		return types.Judgment{}, types.Epoch{}, false, err
	}
	for i := len(eps) - 1; i >= 0; i-- {
		e := eps[i]
		if !e.HasJudgment || (e.State != types.EpochJudged && e.State != types.EpochApplied) {
			continue
		}
		rec, err := h.store.GetJudgment(ctx, h.d.Profile, gen, e.Seq)
		if err != nil {
			return types.Judgment{}, types.Epoch{}, false, err
		}
		j, err := types.DecodeJudgment(rec.Body)
		if err != nil {
			return types.Judgment{}, types.Epoch{}, false, nil // an undecodable body is no judgment: fail closed
		}
		return j, e, true, nil
	}
	return types.Judgment{}, types.Epoch{}, false, nil
}

// finishStep builds the policy state from the database and lets the policy decide. The policy runs as the run's root
// boss (only when a boss is recorded); it can only ask the finish engine to begin, and the engine decides.
func (h *StrategyHost) finishStep(ctx context.Context, gs *genState, run StrategyRun) (*policy.Decision, *policy.Outcome, error) {
	if h.d.Finish == nil || !run.HasBoss {
		return nil, nil, nil
	}
	st := policy.State{RunRunning: run.Running, Caller: bossName(run.Boss), Boss: bossName(run.Boss), CriterionIDs: run.CriterionIDs(),
		ParkedRounds: gs.parks, LastParkedAt: gs.lastParkedAt, FinishInProgress: gs.finishing, GaveUpNotified: gs.gaveUp, Now: h.d.Clock.Now()}
	if j, e, found, err := h.latestJudgment(ctx, run.Generation); err != nil {
		return nil, nil, err
	} else if found {
		st.Judgment, st.JudgmentAt = &j, time.Unix(e.StartedAt, 0)
	}
	if h.d.SubjectChangedAt != nil {
		t, err := h.d.SubjectChangedAt(ctx, run.Generation)
		if err != nil {
			return nil, nil, fmt.Errorf("strategy host: subject time: %w", err)
		}
		st.SubjectChangedAt = t
	} else {
		st.JudgmentAt = time.Time{} // unknown subject time: the judgment is read as stale
	}
	var fo policy.Outcome
	begin := FinishFromBegin(h.d.Finish)
	runner := policy.Runner{Config: h.d.Policy, Sink: &countingSink{h: h, gs: gs, inner: h.policySink(run.Generation)},
		Finish: policy.FinishFunc(func(ctx context.Context) (policy.Outcome, error) {
			o, err := begin(ctx)
			fo = o
			return o, err
		})}
	dec, err := runner.Step(ctx, st)
	switch dec.Action {
	case policy.ActionGiveUp:
		gs.gaveUp = err == nil
	case policy.ActionRequest:
		if err == nil && fo.Accepted {
			gs.finishing = true
		}
		return &dec, &fo, err
	}
	return &dec, nil, err
}

func bossName(id int64) string { return fmt.Sprintf("session:%d", id) }

// countingSink counts parked finish rounds as the policy records them.
type countingSink struct {
	h     *StrategyHost
	gs    *genState
	inner policy.Sink
}

func (c *countingSink) Record(ctx context.Context, kind, detail string) error {
	if err := c.inner.Record(ctx, kind, detail); err != nil {
		return err
	}
	if kind == types.KindFinishParked {
		c.gs.parks++
		c.gs.lastParkedAt = c.h.d.Clock.Now()
	}
	return nil
}
