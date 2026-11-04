package types

import "strings"

// Audit kinds of the strategy layer, reserved for its typed writer (the store seam S3 adds the prefix to the reserved
// list; until then these are names only). A caller outside the typed writer never appends a row of these kinds.
const (
	KindPrefix         = "strategy."
	KindProposal       = "strategy.proposal"
	KindNovelty        = "strategy.novelty"
	KindDirectionState = "strategy.direction.state"
	KindEpochBegin     = "strategy.epoch.begin"
	KindJudgment       = "strategy.judgment"
	KindBadJudgment    = "strategy.bad-judgment"
	KindEpochEnd       = "strategy.epoch.end"
	KindFinishReady    = "strategy.finish.ready" // a marker, never a request: the boss reads it and decides
	KindFinishParked   = "strategy.finish.parked"
	KindFinishGaveUp   = "strategy.finish.gave-up"
	KindParked         = "strategy.parked" // a tampered strategy row was found: strategy stops, the run does not
)

// AllKinds lists every reserved kind, for the reserved-list seam and its pinning test.
func AllKinds() []string {
	return []string{KindProposal, KindNovelty, KindDirectionState, KindEpochBegin, KindJudgment, KindBadJudgment,
		KindEpochEnd, KindFinishReady, KindFinishParked, KindFinishGaveUp, KindParked}
}

// IsReserved reports whether an audit kind belongs to the strategy layer.
func IsReserved(kind string) bool { return strings.HasPrefix(kind, KindPrefix) }
