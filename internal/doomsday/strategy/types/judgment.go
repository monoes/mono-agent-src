package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Judgment is the judge's typed output. The JSON shape is frozen (the control room reads it). It never carries a
// profile, generation or epoch: those come from the host, never from model text.
type Judgment struct {
	Criteria    []CriterionProgress   `json:"criteria"`
	Directions  []DirectionAssessment `json:"directions"`
	NextActions []NextAction          `json:"next_actions"`
}

// CriterionProgress is the judge's reading of one criterion.
type CriterionProgress struct {
	ID     string          `json:"id"`
	Status CriterionStatus `json:"status"`
	Note   string          `json:"note,omitempty"`
	// Count is how many of a counted criterion ("50 applications sent") the judge saw done. Optional: absent is no
	// reading, never zero. It is the judge's opinion, shown as such and never used to decide a finish.
	Count *int `json:"count,omitempty"`
}

// MaxCount bounds a reported count (and a parsed target).
const MaxCount = 1_000_000_000

// DirectionAssessment is the judge's call on one direction.
type DirectionAssessment struct {
	ID      string  `json:"id"`
	Verdict Verdict `json:"verdict"`
	Note    string  `json:"note,omitempty"`
}

// NextAction is a suggested task, optionally for one direction. It is text for planners, never a command.
type NextAction struct {
	DirectionID string `json:"direction_id,omitempty"`
	Text        string `json:"text"`
}

// Expect is what the host knows to be true: the charter's criterion ids and the live direction ids. A judgment is
// checked against it, so the model cannot invent an id or skip a criterion.
type Expect struct {
	CriterionIDs []string
	DirectionIDs []string
}

// ErrBadJudgment wraps every refusal. The caller records a bad-judgment epoch and changes nothing else.
var ErrBadJudgment = errors.New("strategy: bad judgment")

// MaxBadReasonLen bounds, in runes, the reason a bad-judgment line carries.
const MaxBadReasonLen = 120

// BadReason is the short why of a refusal: the validator's own message without the sentinel prefix, control characters
// dropped and cut to MaxBadReasonLen. An error that is not an ErrBadJudgment refusal gives "" (its text may be anything).
func BadReason(err error) string {
	if err == nil || !errors.Is(err, ErrBadJudgment) {
		return ""
	}
	s := strings.TrimPrefix(err.Error(), ErrBadJudgment.Error()+": ")
	out := make([]rune, 0, MaxBadReasonLen)
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if len(out) == MaxBadReasonLen {
			break
		}
		out = append(out, r)
	}
	return string(out)
}

func bad(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadJudgment, fmt.Sprintf(format, a...))
}

// DecodeJudgment decodes strictly: size cap, one JSON value, unknown fields refused, trailing bytes refused.
func DecodeJudgment(body []byte) (Judgment, error) {
	var j Judgment
	if len(body) == 0 || len(body) > MaxJudgmentBytes {
		return j, bad("body size %d", len(body))
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return Judgment{}, bad("decode: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Judgment{}, bad("trailing data")
	}
	return j, nil
}

// Validate checks the judgment against what the host expects. Every expected criterion appears exactly once; no id
// is unknown or repeated; enums are closed; texts are bounded and carry no control characters.
func (j Judgment) Validate(exp Expect) error {
	if len(j.Criteria) > MaxCriteria || len(j.Directions) > MaxDirections || len(j.NextActions) > MaxNextActions {
		return bad("too many entries")
	}
	wantC := toSet(exp.CriterionIDs)
	seen := map[string]bool{}
	for _, c := range j.Criteria {
		switch c.Status {
		case CritNotStarted, CritProgressing, CritPlausiblyMet, CritRegressed:
		default:
			return bad("criterion status %q", c.Status)
		}
		if !wantC[c.ID] || seen[c.ID] || !validText(c.Note, MaxNoteLen, false) ||
			(c.Count != nil && (*c.Count < 0 || *c.Count > MaxCount)) {
			return bad("criterion %q unknown, repeated or with a bad note or count", c.ID)
		}
		seen[c.ID] = true
	}
	if len(seen) != len(wantC) {
		return bad("%d of %d criteria judged", len(seen), len(wantC))
	}
	wantD := toSet(exp.DirectionIDs)
	seen = map[string]bool{}
	for _, d := range j.Directions {
		switch d.Verdict {
		case VerdictKeep, VerdictPrune, VerdictBoost:
		default:
			return bad("direction verdict %q", d.Verdict)
		}
		if !wantD[d.ID] || seen[d.ID] || !validText(d.Note, MaxNoteLen, false) {
			return bad("direction %q unknown, repeated or with a bad note", d.ID)
		}
		seen[d.ID] = true
	}
	for _, a := range j.NextActions {
		if (a.DirectionID != "" && !wantD[a.DirectionID]) || !validText(a.Text, MaxNoteLen, true) {
			return bad("next action refused")
		}
	}
	return nil
}

// AllPlausiblyMet reports whether every one of the given criteria is judged plausibly met. It is false for no
// criteria at all, for a missing id and for any other status: a charter with no criteria never reads as met. It is a
// reading of the judge's opinion only; a finish still needs the finish package's verification.
func (j Judgment) AllPlausiblyMet(criterionIDs []string) bool {
	if len(criterionIDs) == 0 {
		return false
	}
	got := map[string]CriterionStatus{}
	for _, c := range j.Criteria {
		if _, dup := got[c.ID]; dup {
			return false
		}
		got[c.ID] = c.Status
	}
	for _, id := range criterionIDs {
		if got[id] != CritPlausiblyMet {
			return false
		}
	}
	return len(got) == len(toSet(criterionIDs))
}

func toSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func validID(s string) bool { return validText(s, MaxIDLen, true) }

// validText: bounded, valid UTF-8, no control characters other than newline and tab; empty only when allowed.
func validText(s string, max int, nonEmpty bool) bool {
	if len(s) > max || (nonEmpty && s == "") {
		return false
	}
	for _, r := range s {
		if r == '�' || (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
			return false
		}
	}
	return true
}

// Validate checks a proposal before the novelty gate sees it.
func (p Proposal) Validate() error {
	if !validText(p.Title, MaxTitleLen, true) || !validText(p.Rationale, MaxRationaleLen, false) || !validID(p.ProposerSession) {
		return errors.New("strategy: bad proposal")
	}
	if len(p.CriterionIDs) > MaxProposalCrit {
		return errors.New("strategy: too many criteria on a proposal")
	}
	for _, id := range p.CriterionIDs {
		if !validID(id) {
			return errors.New("strategy: bad criterion id on a proposal")
		}
	}
	return nil
}
