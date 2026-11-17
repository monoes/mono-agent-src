package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"unicode"
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

// UnmarshalJSON decodes as strictly as the rest of the body (unknown fields refused) except for the advisory count: a
// count that is out of range or not a plain integer is dropped (no reading), never a reason to refuse the judgment.
func (c *CriterionProgress) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID     string          `json:"id"`
		Status CriterionStatus `json:"status"`
		Note   string          `json:"note"`
		Count  json.RawMessage `json:"count"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	*c = CriterionProgress{ID: raw.ID, Status: raw.Status, Note: raw.Note}
	if n, err := strconv.ParseInt(string(raw.Count), 10, 64); err == nil && n >= 0 && n <= MaxCount {
		v := int(n)
		c.Count = &v
	}
	return nil
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

// badErr is a refusal with a fixed reason. The reason is always one of a small vocabulary written here, never text
// the model supplied (a key, an id, a status or a decoder message that quotes one).
type badErr struct{ reason string }

func (e *badErr) Error() string { return ErrBadJudgment.Error() + ": " + e.reason }
func (e *badErr) Unwrap() error { return ErrBadJudgment }

func bad(reason string) error { return &badErr{reason: reason} }

// BadReason is the short why of a refusal. Only a refusal made here carries one of the fixed reasons; any other error
// wrapping ErrBadJudgment (its text may quote model or transport text) gives a generic one, and an error that is not a
// refusal gives "". The result is cleaned and bounded.
func BadReason(err error) string {
	if err == nil || !errors.Is(err, ErrBadJudgment) {
		return ""
	}
	var be *badErr
	if errors.As(err, &be) {
		return CleanReason(be.reason)
	}
	return "judgment refused"
}

// CleanReason drops control characters (C0, DEL and C1) and invisible format characters (bidi marks and the like) and
// cuts the text to MaxBadReasonLen runes. Everything written as a bad-judgment reason passes through it.
func CleanReason(s string) string {
	out := make([]rune, 0, MaxBadReasonLen)
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if len(out) == MaxBadReasonLen {
			break
		}
		out = append(out, r)
	}
	return string(out)
}

// DecodeJudgment decodes strictly: size cap, one JSON value, unknown fields refused, trailing bytes refused.
func DecodeJudgment(body []byte) (Judgment, error) {
	var j Judgment
	if len(body) == 0 || len(body) > MaxJudgmentBytes {
		return j, bad("bad body size")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return Judgment{}, bad("unknown field")
		}
		return Judgment{}, bad("malformed json")
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
			return bad("bad status")
		}
		if !wantC[c.ID] || seen[c.ID] || !validText(c.Note, MaxNoteLen, false) {
			return bad("unknown criterion")
		}
		seen[c.ID] = true
	}
	if len(seen) != len(wantC) {
		return bad("wrong criterion count")
	}
	wantD := toSet(exp.DirectionIDs)
	seen = map[string]bool{}
	for _, d := range j.Directions {
		switch d.Verdict {
		case VerdictKeep, VerdictPrune, VerdictBoost:
		default:
			return bad("bad verdict")
		}
		if !wantD[d.ID] || seen[d.ID] || !validText(d.Note, MaxNoteLen, false) {
			return bad("unknown direction")
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
