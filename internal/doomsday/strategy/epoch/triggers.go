package epoch

import (
	"context"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Bounds that no configuration can lift.
const (
	DefaultStallWindow  = time.Hour
	MaxStallWindow      = 24 * time.Hour
	DefaultEpochsPerHr  = 6
	MaxEpochsPerHour    = 12
	maxActivityEntries  = types.MaxDirections * 2
	maxEvidenceSeqValue = int64(1) << 62
)

// DirectionActivity is what the host knows about one direction's lead. LastProgress zero means unknown: the direction
// is then never read as stalled. Finished means its lead session is done.
type DirectionActivity struct {
	ID           string
	LastProgress time.Time
	Finished     bool
}

// BudgetShare is the share of the run's budget the strategy lane has used. Known false (the B3 governor is not wired)
// never fires a trigger.
type BudgetShare struct {
	Known bool
	Share float64
}

// Snapshot is everything the engine reads from the host per tick. Text fields are untrusted and bounded by the
// judge package; the counts here are bounded by the engine.
type Snapshot struct {
	Criteria    []judge.CriterionInput
	Summaries   map[string]string // direction id -> short summary for the judge
	Activity    []DirectionActivity
	Budget      BudgetShare
	EvidenceSeq int64 // grows when new evidence arrives; only changes matter
	FinishParks int   // how many times a finish was parked so far in this run (the policy layer's count)
}

// Inputs supplies the Snapshot. It must not block for long and must honour ctx.
type Inputs interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

// candidate is the outcome of reading the triggers: the first that applies, in a fixed priority order.
type candidate struct {
	trigger  types.Trigger
	finished []string // open directions whose lead is done
	stalled  []string // active directions idle for a full stall window
}

type triggerState struct {
	now           time.Time
	last          time.Time // start of the latest epoch, or the engine's start when there is none
	interval      time.Duration
	stallWindow   time.Duration
	threshold     float64
	snap          Snapshot
	open          []types.Direction
	parkedRounds  int
	budgetFired   bool
	evidenceSeen  int64
	evidenceKnown bool
}

// stalledIDs are the active directions with known progress older than the window. The window is already clamped
// to [MinEpochInterval, MaxStallWindow] by New; the scan is limited to maxActivityEntries.
func stalledIDs(now time.Time, window time.Duration, open []types.Direction, act []DirectionActivity) []string {
	active := map[string]bool{}
	for _, d := range open {
		if d.State == types.DirActive {
			active[d.ID] = true
		}
	}
	var out []string
	for i, a := range act {
		if i >= maxActivityEntries {
			break
		}
		if active[a.ID] && !a.Finished && !a.LastProgress.IsZero() && now.Sub(a.LastProgress) >= window {
			out = append(out, a.ID)
		}
	}
	return out
}

func finishedIDs(open []types.Direction, act []DirectionActivity) []string {
	live := map[string]bool{}
	for _, d := range open {
		live[d.ID] = true
	}
	var out []string
	for i, a := range act {
		if i >= maxActivityEntries {
			break
		}
		if live[a.ID] && a.Finished {
			out = append(out, a.ID)
		}
	}
	return out
}

// evaluate reads the triggers in priority order: finish-parked, direction-finished, direction-stalled, budget-share,
// new-evidence, timer. It only reads; the floor and the hourly cap are applied by the caller.
func evaluate(s triggerState) (candidate, bool) {
	c := candidate{
		finished: finishedIDs(s.open, s.snap.Activity),
		stalled:  stalledIDs(s.now, s.stallWindow, s.open, s.snap.Activity),
	}
	switch {
	case s.snap.FinishParks > s.parkedRounds && s.parkedRounds < types.MaxParkedFinishRounds:
		c.trigger = types.TriggerFinishParked
	case len(c.finished) > 0:
		c.trigger = types.TriggerDirectionFinished
	case len(c.stalled) > 0:
		c.trigger = types.TriggerDirectionStalled
	case s.threshold > 0 && s.snap.Budget.Known && s.snap.Budget.Share >= s.threshold && !s.budgetFired:
		c.trigger = types.TriggerBudgetShare
	case s.evidenceKnown && s.snap.EvidenceSeq > s.evidenceSeen && s.snap.EvidenceSeq < maxEvidenceSeqValue:
		c.trigger = types.TriggerNewEvidence
	case s.now.Sub(s.last) >= s.interval:
		c.trigger = types.TriggerTimer
	default:
		return c, false
	}
	return c, true
}
