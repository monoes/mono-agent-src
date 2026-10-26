package coderhost

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The autonomy producer: the only way a session's own model parks a question or records a
// decision. The caller is the Caller of a verified session token and the target is always the
// caller's own session, so a session cannot park, record or answer for another one and the tools
// take no conversation argument. The model supplies the text, the class and the reversible flag,
// and none of them is trusted past what the host validates:
//
//   - the class must be one of GapClasses;
//   - risky_action and missing_credential (HardClass) are never reversible and are never
//     recorded as a model's decision: they are parked, and the master or the user answers;
//   - Park consults the policy (Decide): when it lets the model settle the gap, ParkOwn returns
//     ErrModelDecides and the model is to decide and call RecordOwn;
//   - RecordOwn consults the same policy and refuses a decision the policy reserves for a master
//     or the user, so recording is no way around asking;
//   - made_by is fixed to "model" here, never read from the caller, and the ledger shows it so;
//   - the open-question cap (MaxOpenQuestions), the field caps and a rate of MaxRecordsPerHour
//     records an hour apply, and a model's rows rotate only among the model's own rows.
//
// Neither call starts a turn, queues a command, changes access, a budget or a setting, or
// carries any confirmation: they write one row each. A model that mislabels a gap as reversible
// or low-risk can have it settled by itself under the preset's rules for that class; the rule that
// bounds it is that the ledger shows the decision as the model's, with the flag it claimed, and
// that the operator can revert the reversible ones (hard classes are not claimable at all).

// Rate of model-made records: at most MaxRecordsPerHour per session per hour.
const (
	MaxRecordsPerHour = 20
	recordWindow      = time.Hour
)

// ErrRecordRate: the session has recorded as many decisions in the last hour as it may.
var ErrRecordRate = fmt.Errorf("%w: a session may record at most %d decisions an hour", ErrLimit, MaxRecordsPerHour)

// ErrPolicyReserves: the policy does not let the model settle this gap, so it cannot be recorded
// as the model's decision; it is parked instead.
var ErrPolicyReserves = errors.New("coder host: the policy does not let the model decide this gap; park it as a question instead")

// ParkRequest is a question a session parks about itself.
type ParkRequest struct {
	Class      string
	Question   string
	Options    []string
	Default    string // applied after ParkTimeout only where the policy's fallback is take_default; never for a hard class
	Reversible bool
}

// RecordRequest is a decision a session made for itself.
type RecordRequest struct {
	Class        string
	Question     string
	Choice       string
	Alternatives []string
	Reason       string
	Reversible   bool
}

func validClass(class string) error {
	if !oneOf(class, GapClasses) {
		return fmt.Errorf("%w: %q is not a class of gap (one of %s)", ErrInvalid, class, strings.Join(GapClasses, ", "))
	}
	return nil
}

func (s *Store) ownCaller(c Caller) error {
	if c.ProfileID == "" || c.ConversationID == "" {
		return ErrSessionToken
	}
	return nil
}

// ParkOwn parks a question about the caller's own session. ErrModelDecides means the policy lets
// the model settle it: decide, then RecordOwn.
func (s *Store) ParkOwn(c Caller, r ParkRequest) (Question, error) {
	if err := s.ownCaller(c); err != nil {
		return Question{}, err
	}
	if err := validClass(r.Class); err != nil {
		return Question{}, err
	}
	if strings.TrimSpace(r.Question) == "" {
		return Question{}, fmt.Errorf("%w: a question needs a text", ErrInvalid)
	}
	if HardClass(r.Class) && r.Reversible {
		return Question{}, fmt.Errorf("%w: a %s gap is never reversible", ErrInvalid, r.Class)
	}
	return s.Park(c.ProfileID, c.ConversationID, r.Class, r.Question, r.Options, r.Default, r.Reversible)
}

// RecordOwn writes a decision the caller's own session made without asking, as the model's.
func (s *Store) RecordOwn(c Caller, r RecordRequest) (LedgerEntry, error) {
	if err := s.ownCaller(c); err != nil {
		return LedgerEntry{}, err
	}
	if err := validClass(r.Class); err != nil {
		return LedgerEntry{}, err
	}
	if strings.TrimSpace(r.Choice) == "" {
		return LedgerEntry{}, fmt.Errorf("%w: a decision needs a choice", ErrInvalid)
	}
	if HardClass(r.Class) {
		return LedgerEntry{}, fmt.Errorf("%w (%s gaps are parked, never decided by the model)", ErrPolicyReserves, r.Class)
	}
	settings, err := s.GetSettings(c.ProfileID, c.ConversationID)
	if err != nil {
		return LedgerEntry{}, err
	}
	_, has := s.hasMaster(c.ProfileID, c.ConversationID)
	if Decide(settings, r.Class, r.Reversible, has).Answerer != WhoModel {
		return LedgerEntry{}, ErrPolicyReserves
	}
	var recent int
	since := s.now().Add(-recordWindow).UTC().Format(timeLayout)
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_decisions WHERE profile_id = ? AND conversation_id = ? AND made_by = 'model' AND created_at >= ?`,
		c.ProfileID, c.ConversationID, since).Scan(&recent); err != nil {
		return LedgerEntry{}, err
	}
	if recent >= MaxRecordsPerHour {
		return LedgerEntry{}, ErrRecordRate
	}
	return s.Record(LedgerEntry{ProfileID: c.ProfileID, ConversationID: c.ConversationID, Class: r.Class, Question: r.Question,
		Choice: r.Choice, Alternatives: r.Alternatives, Reason: r.Reason, Reversible: r.Reversible, MadeBy: WhoModel})
}
