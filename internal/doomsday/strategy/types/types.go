// Package types holds the data model of the open-ended mode's strategy layer: directions, epochs, judgments, the
// novelty ledger, their canonical encoding and the closed vocabularies. It is a plain library: no init, no goroutine,
// no I/O, no registration. Nothing here can start, stop or finish a run; a judgment is only a record, and the finish
// decision stays with the finish package.
package types

import "time"

// Timing and bound decisions settled by the owner (B4 plan, Q1, Q4, Q5). They are code constants so an agent or a
// charter cannot lower a floor or raise a cap.
const (
	DefaultEpochInterval  = 30 * time.Minute // timer cadence
	MinEpochInterval      = 5 * time.Minute  // code floor between two epochs, whatever the trigger
	MaxParkedFinishRounds = 5                // then strategy stops requesting a finish and notifies the operator
	MaxUncheckedPerEpoch  = 1                // novelty store down: at most one unchecked proposal per epoch
)

// Size caps for every text and list a model or a planner can hand in.
const (
	MaxTitleLen      = 200
	MaxRationaleLen  = 2000
	MaxNoteLen       = 1000
	MaxIDLen         = 128
	MaxCriteria      = 64
	MaxDirections    = 64
	MaxNextActions   = 16
	MaxProposalCrit  = 16
	MaxJudgmentBytes = 64 * 1024
)

// DirectionState is where a direction stands. Moves are forward only (see CanMoveDirection); finished and pruned are
// terminal. Pruning stops new spawns and queued commands; it never kills a running session (owner decision Q7).
type DirectionState string

const (
	DirProposed DirectionState = "proposed"
	DirActive   DirectionState = "active"
	DirStalled  DirectionState = "stalled"
	DirFinished DirectionState = "finished"
	DirPruned   DirectionState = "pruned"
)

// EpochState: open -> judged -> applied, or open -> bad-judgment / abandoned. Forward only.
type EpochState string

const (
	EpochOpen        EpochState = "open"
	EpochJudged      EpochState = "judged"
	EpochApplied     EpochState = "applied"
	EpochBadJudgment EpochState = "bad-judgment" // the judge's output failed validation; nothing was changed
	EpochAbandoned   EpochState = "abandoned"    // the run stopped before the epoch completed
)

// Trigger is why an epoch started.
type Trigger string

const (
	TriggerTimer             Trigger = "timer"
	TriggerDirectionFinished Trigger = "direction-finished"
	TriggerDirectionStalled  Trigger = "direction-stalled"
	TriggerBudgetShare       Trigger = "budget-share"
	TriggerNewEvidence       Trigger = "new-evidence"
	TriggerFinishParked      Trigger = "finish-parked"
)

// NoveltyOutcome is what the novelty gate decided about a proposal, recorded in the ledger.
type NoveltyOutcome string

const (
	NoveltyAccepted          NoveltyOutcome = "accepted"
	NoveltyAcceptedUnchecked NoveltyOutcome = "accepted-unchecked" // corpus unreachable; at most MaxUncheckedPerEpoch per epoch
	NoveltyRefusedLedger     NoveltyOutcome = "refused-ledger"     // same key as an earlier proposal
	NoveltyRefusedDirection  NoveltyOutcome = "refused-direction"  // matches an open or pruned direction
	NoveltyRefusedCorpus     NoveltyOutcome = "refused-corpus"     // memory or knowledge hit above the threshold
)

// CriterionStatus is the judge's reading of one success criterion. Only a judgment, never a verification.
type CriterionStatus string

const (
	CritNotStarted   CriterionStatus = "not-started"
	CritProgressing  CriterionStatus = "progressing"
	CritPlausiblyMet CriterionStatus = "plausibly-met"
	CritRegressed    CriterionStatus = "regressed"
)

// Verdict is the judge's call on one direction.
type Verdict string

const (
	VerdictKeep  Verdict = "keep"
	VerdictPrune Verdict = "prune"
	VerdictBoost Verdict = "boost"
)

// Proposal is what a planner submits. The title is hashed (after the novelty package's normalisation) into the key.
type Proposal struct {
	Title           string
	Rationale       string
	CriterionIDs    []string // criteria the direction is meant to advance
	ProposerSession string
}

// Direction is one stored line of work. KeyHash is KeyHash(normalised title); UNIQUE per run in the database.
type Direction struct {
	Profile         string
	Generation      int64
	ID              string
	KeyHash         [32]byte
	Title           string
	State           DirectionState
	ProposerSession string
	SessionID       string // the lead session once activated, "" before
	CreatedEpoch    int64
}

// Epoch is one judging round. Its identity is (profile, generation, seq): a restart reuses the same id, never a new one.
type Epoch struct {
	Profile      string
	Generation   int64
	Seq          int64
	Trigger      Trigger
	StartedAt    int64 // unix seconds
	JudgmentHash [32]byte
	HasJudgment  bool
	ModelPin     string // provider/model/version of the judge, plus whether it equals the boss model (recorded, allowed)
	State        EpochState
}

// JudgmentRecord is the stored judgment: the raw body bytes the judge returned (already validated) and their hash.
type JudgmentRecord struct {
	Profile    string
	Generation int64
	EpochSeq   int64
	Body       []byte
	BodyHash   [32]byte
}

// NoveltyRow is one ledger entry. DirectionID is the direction created (accepted) or "" (refused); MatchedID names the
// ledger or direction row a refusal cites.
type NoveltyRow struct {
	Profile     string
	Generation  int64
	EpochSeq    int64
	KeyHash     [32]byte
	DirectionID string
	MatchedID   string
	Outcome     NoveltyOutcome
}

func validDirState(s DirectionState) bool {
	switch s {
	case DirProposed, DirActive, DirStalled, DirFinished, DirPruned:
		return true
	}
	return false
}

// CanMoveDirection reports whether from -> to is a legal forward move. It is the Go twin of the migration's trigger.
func CanMoveDirection(from, to DirectionState) bool {
	switch from {
	case DirProposed:
		return to == DirActive || to == DirPruned
	case DirActive:
		return to == DirStalled || to == DirFinished || to == DirPruned
	case DirStalled:
		return to == DirFinished || to == DirPruned
	}
	return false
}

// CanMoveEpoch is the Go twin of the epoch trigger.
func CanMoveEpoch(from, to EpochState) bool {
	switch from {
	case EpochOpen:
		return to == EpochJudged || to == EpochBadJudgment || to == EpochAbandoned
	case EpochJudged:
		return to == EpochApplied || to == EpochAbandoned
	}
	return false
}
