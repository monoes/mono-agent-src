// Package finish implements the finish-commit predicate and the verifier review of note 13.6.
//
// It is self-contained and codes against the narrow Ledger and Verifier interfaces; the store package will
// satisfy Ledger inside the sequencer's critical section. The package makes nothing reachable: the mode ships
// off, and a charter with no criteria, or no distinct pinned verifier, can never finish.
package finish

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Pin is the identity of a model as the host reports it (note 13.6 section 9). Never an alias.
type Pin struct {
	Provider   string
	Model      string
	Version    string
	PolicyHash string // hash of the purpose policy in force at entry for the verification purpose
}

func (p Pin) complete() bool {
	return p.Provider != "" && p.Model != "" && p.Version != "" && p.PolicyHash != ""
}

func (p Pin) moving() bool {
	for _, s := range []string{p.Model, p.Version} {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "latest", "default", "auto", "stable", "current":
			return true
		}
	}
	return false
}

// SameModel compares (provider, model identifier): the distinctness test of Q3.
func (p Pin) SameModel(o Pin) bool {
	return strings.EqualFold(p.Provider, o.Provider) && strings.EqualFold(p.Model, o.Model)
}

// Criterion is the part of a charter criterion the finish needs: its id, its hash and whether a person decides.
type Criterion struct {
	ID     string
	Hash   string
	Rubric bool
}

// Charter is what the finish reads from the charter (bound by the charter hash).
type Charter struct {
	Hash      string
	Criteria  []Criterion
	Verifier  Pin
	Boss      Pin
	Roster    []Pin // every model the charter allows for work sessions
	MaxRounds int
}

// CannotFinish says, in the words of the entry dialog, why a charter can never finish ("" when it can).
func (c Charter) CannotFinish() string {
	switch {
	case len(c.Criteria) == 0:
		return "this charter has no criteria, so it cannot finish"
	case !c.Verifier.complete() || c.Verifier.moving():
		return "this charter has no pinned verifier (provider, model identifier, version and policy), so it cannot finish"
	case c.Verifier.SameModel(c.Boss):
		return "the verifier is the same model as the boss, so this charter cannot finish"
	}
	for _, m := range c.Roster {
		if c.Verifier.SameModel(m) {
			return "the verifier is a model allowed for work sessions, so this charter cannot finish"
		}
	}
	return ""
}

// Round is the current verification round, read from the run row and the freeze transition.
type Round struct {
	N         int64
	FreezeSeq int64
	Digest    string
	CloseSeq  int64 // s_close: the seq of finish-begin
}

// Evidence is one chained evidence row (note 13.6 section 8), reduced to what the predicate compares.
type Evidence struct {
	Seq           int64
	Generation    int64
	CharterHash   string
	Round         int64
	FreezeSeq     int64
	Digest        string
	CriterionID   string
	CriterionHash string
	Result        string // pass, fail, error, timeout, oversize, tainted, tampered
	Advisory      bool
	Verifier      Pin
	StdoutDigest  string
	StderrDigest  string
	// The environment descriptor and bounded excerpts for the person (note 13.6 section 8). The digests are the
	// evidence; the excerpts are not.
	ExitCode        int      `json:",omitempty"`
	Net             string   `json:",omitempty"`
	Detail          string   `json:",omitempty"`
	StdoutExcerpt   string   `json:",omitempty"`
	StderrExcerpt   string   `json:",omitempty"`
	RunsSubjectCode bool     `json:",omitempty"`
	Tainted         []string `json:",omitempty"`
}

// RubricDecision is an operator decision row bound to the version it was shown.
type RubricDecision struct {
	Generation    int64
	CharterHash   string
	Round         int64
	FreezeSeq     int64
	Digest        string
	CriterionID   string
	CriterionHash string
	Approve       bool
	// DossierHash is the hash of the dossier the person was shown (note 13.6 section 10); Note is their bounded reason.
	DossierHash string `json:",omitempty"`
	Note        string `json:",omitempty"`
}

// Review states. A review is two chained rows: started, then complete. Only a complete row can count.
const (
	ReviewStarted  = "started"
	ReviewComplete = "complete"
)

// Closed verdict enumeration: the harness consumes nothing else from the model (section 9).
const (
	VerdictOK    = "ok"
	VerdictBlock = "block"
)

// Review is a verifier review row. ResultsDigest binds it to the evidence it read.
type Review struct {
	Seq           int64
	Generation    int64
	CharterHash   string
	Round         int64
	FreezeSeq     int64
	Digest        string
	State         string
	Verdict       string // VerdictOK or VerdictBlock (complete rows only)
	ReasonCode    string
	Observed      Pin // the tuple the provider reported for this call
	ResultsDigest string
}

// ResultsDigest is the digest of the round's machine results as the verifier read them. It covers the
// criterion identity, its hash, the outcome and the output digests, in a fixed order.
func ResultsDigest(ev []Evidence) string {
	lines := make([]string, 0, len(ev))
	for _, e := range ev {
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%s|%s", e.CriterionID, e.CriterionHash, e.Result, e.StdoutDigest, e.StderrDigest))
	}
	sort.Strings(lines)
	h := sha256.Sum256([]byte("monoagent-finish-results-v1\x00" + strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:])
}

// Finish states as the ledger reports them in Facts.State (note 13.6 section 7.1). Only StateVerifying can commit.
const (
	StateVerifying = "verifying"
	StateFinished  = "finished"
	StateParked    = "finish-parked"
)

// Facts is everything the finish-commit predicate reads, taken under the sequencer's locks.
type Facts struct {
	Generation int64
	State      string // the run row: must be "verifying"
	// ParkReason is the reason of the park that left the run in StateParked ("" otherwise).
	ParkReason Reason
	Charter    Charter
	Round      Round
	Evidence   []Evidence       // all chained evidence rows of the generation (earlier rounds included)
	Decisions  []RubricDecision // all rubric decisions of the generation
	Reviews    []Review         // all review rows of the generation
	// WorkRowsAfterClose counts rows of any work-admitting kind with seq > Round.CloseSeq in this generation.
	WorkRowsAfterClose int
	// RecheckOK: the last live recheck (13.6 section 7.3) succeeded after the last evidence row.
	RecheckOK bool
}

// Reason is a not-finished reason from the closed list of section 13.
type Reason string

const (
	ReasonCriteriaFailed      Reason = "criteria-failed"
	ReasonCriterionError      Reason = "criterion-error"
	ReasonTainted             Reason = "tainted"
	ReasonTampered            Reason = "tampered"
	ReasonSubjectChanged      Reason = "subject-changed"
	ReasonVerifierBlocked     Reason = "verifier-blocked"
	ReasonVerifierUnavailable Reason = "verifier-unavailable"
	ReasonVerifierChanged     Reason = "verifier-changed"
	ReasonRubricRejected      Reason = "rubric-rejected"
	ReasonInternalError       Reason = "internal-error"
)

// Outcome of the predicate.
type Outcome int

const (
	// Commit: every condition holds; the finish-commit rows may be inserted.
	Commit Outcome = iota + 1
	// Wait: something is not there yet (a review, a rubric decision, the recheck); nothing is inserted and
	// nothing is parked. No timeout resolves a wait.
	Wait
	// Park: a named, final-for-this-round reason; the caller writes finish-park.
	Park
)

// Verdict is the predicate's answer with its reason.
type Verdict struct {
	Outcome Outcome
	Reason  Reason
	Detail  string
}

// Wait reasons: the finish is not decided yet; nothing is parked and nothing resolves them by timeout.
const (
	WaitEvidence Reason = "evidence-pending"
	WaitRubric   Reason = "rubric-pending"
	WaitReview   Reason = "review-pending"
	WaitRecheck  Reason = "recheck-pending"
)
