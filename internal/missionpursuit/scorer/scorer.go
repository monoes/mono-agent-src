package scorer

import (
	"context"
	"errors"

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

// Score is the stub; it is replaced by the real implementation.
func (s *Scorer) Score(ctx context.Context, d types.Direction) (float64, error) {
	return 0, nil
}

type crit struct{ success, cost, risk, effort float64 }

var weights = [4]float64{3, 1, 1, 1}

func criteria(f DirectionFacts) crit { return crit{} }
