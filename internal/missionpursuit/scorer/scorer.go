package scorer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// ErrUnscored is returned (wrapped) whenever a direction cannot be scored from host facts.
var ErrUnscored = errors.New("scorer: direction is unscored")

// MaxCount bounds every count in DirectionFacts; a larger one is refused as unreadable.
const MaxCount = 100000

// DirectionFacts are the host-measured numbers of one direction. It holds no text, so no agent words can reach a score.
type DirectionFacts struct {
	TasksDone, TasksFailed, TasksOpen, TasksBlocked int
	CriteriaChecked, CriteriaPassed                 int // typed checks the host ran and recorded
	EvidenceRows                                    int // host-recorded evidence rows
	SpendKnown                                      bool
	SpendPermille                                   int // share of the direction's budget used, 0..1000+ (over budget reads as 1000)
}

// Facts reads the host rows of one direction, by its lead session id.
type Facts interface {
	DirectionFacts(ctx context.Context, sessionID string) (DirectionFacts, error)
}

// Scorer implements the epoch policy's Scorer over host facts.
type Scorer struct{ facts Facts }

// New returns a Scorer over facts. A nil facts source scores everything as unscored.
func New(f Facts) *Scorer { return &Scorer{facts: f} }

// Score scores d from the host facts of its lead session; higher is better, in [0, 10]. Anything it cannot read or
// trust is ErrUnscored (wrapped), which the ranking places last. Nothing but d.SessionID is looked at.
func (s *Scorer) Score(ctx context.Context, d types.Direction) (float64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	id := strings.TrimSpace(d.SessionID)
	if s == nil || s.facts == nil || id == "" {
		return 0, fmt.Errorf("%w: no session or no facts source", ErrUnscored)
	}
	f, err := s.facts.DirectionFacts(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("%w: facts unreadable", ErrUnscored)
	}
	if err := f.check(); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrUnscored, err)
	}
	c := criteria(f)
	sum := weights[0] + weights[1] + weights[2] + weights[3]
	v := (weights[0]*c.success + weights[1]*(maxScore-c.cost) + weights[2]*(maxScore-c.risk) + weights[3]*(maxScore-c.effort)) / sum
	return v, nil // every criterion is in [0, 10] by construction, so is the weighted mean
}

const (
	maxScore    = 10.0
	effortCap   = 20 // open plus blocked tasks that read as the worst effort
	evidenceCap = 5  // evidence rows that read as full evidence
)

// check refuses facts that are out of bounds or contradict themselves, and facts with no host-observed activity.
func (f DirectionFacts) check() error {
	for _, n := range []int{f.TasksDone, f.TasksFailed, f.TasksOpen, f.TasksBlocked, f.CriteriaChecked, f.CriteriaPassed, f.EvidenceRows} {
		if n < 0 || n > MaxCount {
			return errors.New("a count is out of bounds")
		}
	}
	if f.CriteriaPassed > f.CriteriaChecked {
		return errors.New("more criteria passed than checked")
	}
	if f.SpendKnown && f.SpendPermille < 0 {
		return errors.New("negative spend")
	}
	if f.TasksDone+f.TasksFailed+f.TasksOpen+f.TasksBlocked+f.CriteriaChecked+f.EvidenceRows == 0 {
		return errors.New("no host-observed activity")
	}
	return nil
}

type crit struct{ success, cost, risk, effort float64 }

// weights are mission_evaluate's DefaultWeights in the order success, cost, risk, effort.
var weights = [4]float64{3, 1, 1, 1}

// criteria derives mission_evaluate's four 0..10 criteria from checked facts. For cost, risk and effort higher is worse.
func criteria(f DirectionFacts) crit {
	total := f.TasksDone + f.TasksFailed + f.TasksOpen + f.TasksBlocked
	var critFrac, doneFrac float64
	if f.CriteriaChecked > 0 {
		critFrac = float64(f.CriteriaPassed) / float64(f.CriteriaChecked)
	}
	if total > 0 {
		doneFrac = float64(f.TasksDone) / float64(total)
	}
	evid := float64(min(f.EvidenceRows, evidenceCap)) / evidenceCap
	c := crit{success: maxScore * (0.6*critFrac + 0.3*doneFrac + 0.1*evid)}
	c.cost = maxScore // an unknown spend reads as the worst
	if f.SpendKnown {
		c.cost = maxScore * float64(min(f.SpendPermille, 1000)) / 1000
	}
	c.risk = maxScore // no finished task: unproven
	if fin := f.TasksDone + f.TasksFailed; fin > 0 {
		c.risk = maxScore * float64(f.TasksFailed) / float64(fin)
	}
	c.effort = maxScore * float64(min(f.TasksOpen+f.TasksBlocked, effortCap)) / effortCap
	return c
}
