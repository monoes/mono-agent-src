package epoch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/sstore"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Typed errors. None carries model text or row content.
var (
	ErrBadConfig     = errors.New("epoch: bad configuration")
	ErrInputTooLarge = errors.New("epoch: the host's snapshot exceeds the input bounds")
)

// Store is the subset of *sstore.Store the engine uses.
type Store interface {
	LatestEpoch(ctx context.Context, profile string, gen int64) (types.Epoch, bool, error)
	NextEpochSeq(ctx context.Context, profile string, gen int64) (int64, error)
	ListEpochs(ctx context.Context, profile string, gen int64) ([]types.Epoch, error)
	InsertEpoch(ctx context.Context, e types.Epoch) (types.Epoch, bool, error)
	RecordJudgment(ctx context.Context, profile string, gen, seq int64, body []byte) (bool, error)
	GetJudgment(ctx context.Context, profile string, gen, seq int64) (types.JudgmentRecord, error)
	MoveEpoch(ctx context.Context, profile string, gen, seq int64, to types.EpochState) error
	OpenDirections(ctx context.Context, profile string, gen int64) ([]types.Direction, error)
	MoveDirection(ctx context.Context, profile string, gen int64, id string, to types.DirectionState, sessionID string) error
	ParkedFinishRounds(ctx context.Context, profile string, gen int64) (int, error)
}

// Judge is the subset of *judge.Judge the engine uses.
type Judge interface {
	CheckPin() (string, error)
	Run(ctx context.Context, in judge.Input) (judge.Result, error)
}

// RunState answers whether the run is still running. The caller backs it with the store's run state (the barrier),
// not with a local flag. An error is read as "not known": the engine does nothing.
type RunState interface {
	Running(ctx context.Context) (bool, error)
}

var (
	_ Store = (*sstore.Store)(nil)
	_ Judge = (*judge.Judge)(nil)
)

// Config identifies the run and sets the cadence. Every duration and count is clamped to a code bound.
type Config struct {
	Profile    string
	Generation int64

	Interval             time.Duration // timer cadence; 0 = DefaultEpochInterval; below MinEpochInterval = MinEpochInterval
	StallWindow          time.Duration // idle time that makes an active direction stalled; clamped to [MinEpochInterval, MaxStallWindow]
	BudgetShareThreshold float64       // 0 = trigger off
	MaxEpochsPerHour     int           // 0 = DefaultEpochsPerHr; above MaxEpochsPerHour = MaxEpochsPerHour
}

// Deps are the injected collaborators; all are required.
type Deps struct {
	Store  Store
	Judge  Judge
	Inputs Inputs
	Run    RunState
	Clock  Clock
}

// Engine runs epochs. Tick calls are serialised; the engine starts no goroutine of its own.
type Engine struct {
	cfg   Config
	d     Deps
	start time.Time

	mu            sync.Mutex
	parked        bool // a stored row failed verification: strategy stops, the run does not
	retryAt       time.Time
	budgetFired   bool
	budgetInit    bool
	evidenceSeen  int64
	evidenceKnown bool
}

// New validates the configuration and clamps the bounds.
func New(c Config, d Deps) (*Engine, error) {
	if c.Profile == "" || c.Generation < 1 {
		return nil, fmt.Errorf("%w: profile and generation", ErrBadConfig)
	}
	if d.Store == nil || d.Judge == nil || d.Inputs == nil || d.Run == nil || d.Clock == nil {
		return nil, fmt.Errorf("%w: every dependency is required", ErrBadConfig)
	}
	if c.BudgetShareThreshold < 0 || c.BudgetShareThreshold > 1 || c.BudgetShareThreshold != c.BudgetShareThreshold {
		return nil, fmt.Errorf("%w: budget share threshold", ErrBadConfig)
	}
	if c.Interval <= 0 {
		c.Interval = types.DefaultEpochInterval
	}
	if c.Interval < types.MinEpochInterval {
		c.Interval = types.MinEpochInterval
	}
	if c.StallWindow <= 0 {
		c.StallWindow = DefaultStallWindow
	}
	if c.StallWindow < types.MinEpochInterval {
		c.StallWindow = types.MinEpochInterval
	}
	if c.StallWindow > MaxStallWindow {
		c.StallWindow = MaxStallWindow
	}
	if c.MaxEpochsPerHour <= 0 {
		c.MaxEpochsPerHour = DefaultEpochsPerHr
	}
	if c.MaxEpochsPerHour > MaxEpochsPerHour {
		c.MaxEpochsPerHour = MaxEpochsPerHour
	}
	return &Engine{cfg: c, d: d, start: d.Clock.Now()}, nil
}

// Tick advances the engine by at most one epoch. Order for a new epoch: insert (idempotent id), judge once, record,
// apply, mark applied. A host restart finds the epoch in whatever stage it reached and continues it; an already
// judged epoch is never judged again. After the run stopped Tick writes nothing new: it only closes an unfinished
// epoch as abandoned.
func (e *Engine) Tick(ctx context.Context) (TickResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.parked {
		return TickResult{Outcome: OutcomeInert, Held: HeldParked}, nil
	}
	running, err := e.d.Run.Running(ctx)
	if err != nil {
		return TickResult{Outcome: OutcomeIdle, Held: HeldRunUnknown}, err
	}
	latest, have, err := e.d.Store.LatestEpoch(ctx, e.cfg.Profile, e.cfg.Generation)
	if err != nil {
		return e.fail(err)
	}
	unfinished := have && (latest.State == types.EpochOpen || latest.State == types.EpochJudged)
	if !running {
		return e.inert(ctx, latest, unfinished)
	}
	if unfinished {
		return e.resume(ctx, latest)
	}
	return e.begin(ctx, latest, have)
}

// fail turns a store error into a result; a tampered row parks the strategy.
func (e *Engine) fail(err error) (TickResult, error) {
	if errors.Is(err, sstore.ErrTampered) {
		e.parked = true
		return TickResult{Outcome: OutcomeInert, Held: HeldParked}, err
	}
	return TickResult{Outcome: OutcomeIdle}, err
}

func (e *Engine) inert(ctx context.Context, latest types.Epoch, unfinished bool) (TickResult, error) {
	res := TickResult{Outcome: OutcomeInert, Held: HeldNotRunning}
	if !unfinished {
		return res, nil
	}
	if err := e.d.Store.MoveEpoch(ctx, e.cfg.Profile, e.cfg.Generation, latest.Seq, types.EpochAbandoned); err != nil {
		return e.fail(err)
	}
	res.EpochSeq, res.Trigger, res.Abandoned = latest.Seq, latest.Trigger, true
	return res, nil
}

func (e *Engine) stillRunning(ctx context.Context) bool {
	ok, err := e.d.Run.Running(ctx)
	return err == nil && ok
}

// begin evaluates the triggers and, when one applies and the floor and the hourly cap allow, opens a new epoch.
func (e *Engine) begin(ctx context.Context, latest types.Epoch, haveLatest bool) (TickResult, error) {
	now := e.d.Clock.Now()
	if now.Before(e.retryAt) {
		return TickResult{Outcome: OutcomeIdle, Held: HeldBackoff}, nil
	}
	snap, err := e.d.Inputs.Snapshot(ctx)
	if err != nil {
		return TickResult{Outcome: OutcomeIdle}, fmt.Errorf("epoch: snapshot: %w", err)
	}
	if err := checkBounds(snap); err != nil {
		return TickResult{Outcome: OutcomeIdle}, err
	}
	open, err := e.d.Store.OpenDirections(ctx, e.cfg.Profile, e.cfg.Generation)
	if err != nil {
		return e.fail(err)
	}
	parked, err := e.d.Store.ParkedFinishRounds(ctx, e.cfg.Profile, e.cfg.Generation)
	if err != nil {
		return e.fail(err)
	}
	last := e.start
	if haveLatest {
		last = time.Unix(latest.StartedAt, 0)
	}
	e.prime(snap, latest, haveLatest)
	cand, ok := evaluate(triggerState{now: now, last: last, interval: e.cfg.Interval, stallWindow: e.cfg.StallWindow,
		threshold: e.cfg.BudgetShareThreshold, snap: snap, open: open, parkedRounds: parked, budgetFired: e.budgetFired,
		evidenceSeen: e.evidenceSeen, evidenceKnown: e.evidenceKnown})
	if !ok {
		return TickResult{Outcome: OutcomeIdle}, nil
	}
	if now.Sub(last) < types.MinEpochInterval {
		return TickResult{Outcome: OutcomeIdle, Trigger: cand.trigger, Held: HeldFloor}, nil
	}
	if held, err := e.overHourlyCap(ctx, now); err != nil {
		return e.fail(err)
	} else if held {
		return TickResult{Outcome: OutcomeIdle, Trigger: cand.trigger, Held: HeldHourlyCap}, nil
	}
	pin, err := e.d.Judge.CheckPin()
	if err != nil {
		e.retryAt = now.Add(types.MinEpochInterval)
		return TickResult{Outcome: OutcomeIdle, Trigger: cand.trigger, Held: HeldJudgePin}, err
	}
	seq, err := e.d.Store.NextEpochSeq(ctx, e.cfg.Profile, e.cfg.Generation)
	if err != nil {
		return e.fail(err)
	}
	if !e.stillRunning(ctx) {
		return TickResult{Outcome: OutcomeInert, Held: HeldNotRunning}, nil
	}
	ep, _, err := e.d.Store.InsertEpoch(ctx, types.Epoch{Profile: e.cfg.Profile, Generation: e.cfg.Generation, Seq: seq,
		Trigger: cand.trigger, StartedAt: now.Unix(), ModelPin: pin, State: types.EpochOpen})
	if err != nil {
		return e.fail(err)
	}
	if cand.trigger == types.TriggerBudgetShare {
		e.budgetFired = true
	}
	e.evidenceSeen = snap.EvidenceSeq
	return e.judgeEpoch(ctx, ep, snap, false)
}

// prime sets the in-memory edge detectors on the first tick: the evidence baseline (earlier evidence is not news) and
// whether a budget-share epoch already covers the current crossing.
func (e *Engine) prime(snap Snapshot, latest types.Epoch, haveLatest bool) {
	if !e.evidenceKnown {
		e.evidenceSeen, e.evidenceKnown = snap.EvidenceSeq, true
	}
	if !e.budgetInit {
		e.budgetFired = haveLatest && latest.Trigger == types.TriggerBudgetShare
		e.budgetInit = true
	}
	if !snap.Budget.Known || snap.Budget.Share < e.cfg.BudgetShareThreshold {
		e.budgetFired = false
	}
}

func (e *Engine) overHourlyCap(ctx context.Context, now time.Time) (bool, error) {
	all, err := e.d.Store.ListEpochs(ctx, e.cfg.Profile, e.cfg.Generation)
	if err != nil {
		return false, err
	}
	n := 0
	for _, x := range all {
		if now.Sub(time.Unix(x.StartedAt, 0)) < time.Hour {
			n++
		}
	}
	return n >= e.cfg.MaxEpochsPerHour, nil
}

func checkBounds(s Snapshot) error {
	if len(s.Criteria) > types.MaxCriteria || len(s.Activity) > maxActivityEntries || len(s.Summaries) > types.MaxDirections*2 {
		return ErrInputTooLarge
	}
	return nil
}

// resume continues an epoch the store shows as unfinished. A judged epoch is applied from its stored judgment and the
// judge is not called; an open one is judged (once) under the same id.
func (e *Engine) resume(ctx context.Context, ep types.Epoch) (TickResult, error) {
	if ep.State == types.EpochJudged {
		rec, err := e.d.Store.GetJudgment(ctx, e.cfg.Profile, e.cfg.Generation, ep.Seq)
		if err != nil {
			return e.fail(err)
		}
		j, err := types.DecodeJudgment(rec.Body)
		if err != nil {
			if mv := e.d.Store.MoveEpoch(ctx, e.cfg.Profile, e.cfg.Generation, ep.Seq, types.EpochAbandoned); mv != nil {
				return e.fail(mv)
			}
			return TickResult{Outcome: OutcomeAbandoned, EpochSeq: ep.Seq, Trigger: ep.Trigger, Resumed: true, Abandoned: true}, err
		}
		return e.apply(ctx, ep, j, true)
	}
	snap, err := e.d.Inputs.Snapshot(ctx)
	if err != nil {
		return TickResult{Outcome: OutcomeIdle, EpochSeq: ep.Seq}, fmt.Errorf("epoch: snapshot: %w", err)
	}
	if err := checkBounds(snap); err != nil {
		return TickResult{Outcome: OutcomeIdle, EpochSeq: ep.Seq}, err
	}
	return e.judgeEpoch(ctx, ep, snap, true)
}
