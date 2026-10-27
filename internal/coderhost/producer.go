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
//   - a session with no master (solo, master, orphaned child, or a child whose master is stopped or gone) has nobody to ask but the person, so
//     ParkOwn refuses to park any gap but a hard one (ErrNoMasterDecide): the model decides and
//     records it, or stops. A hard gap is parked for the person and waits without a timeout;
//   - RecordOwn records any class but a hard one, whatever the preset (so ask-always governs
//     parking only); the ledger (made by the model, the claimed flag, revert) is the control;
//   - made_by is fixed to "model" here, never read from the caller, and the ledger shows it so;
//   - the open-question cap (MaxOpenQuestions), the field caps and a rate of MaxRecordsPerHour
//     records an hour apply, and a model's rows rotate only among the model's own rows.
//
// Neither call starts a turn, queues a command, changes access, a budget or a setting, or
// carries any confirmation: they write one row each. A model that mislabels a gap as reversible
// or low-risk can have it recorded as its own; the claimed flag is trusted, and what bounds it is
// that the ledger shows the decision as the model's, with the flag it claimed, and that the operator
// can revert the reversible ones (hard classes are not claimable at all).

// Rate of model-made records: at most MaxRecordsPerHour per session per hour.
const (
	MaxRecordsPerHour = 20
	recordWindow      = time.Hour
)

// ErrRecordRate: the session has recorded as many decisions in the last hour as it may.
var ErrRecordRate = fmt.Errorf("%w: a session may record at most %d decisions an hour", ErrLimit, MaxRecordsPerHour)

// ErrPolicyReserves: the model may never settle this gap (a hard class), so it cannot be recorded
// as the model's decision; it is parked instead.
var ErrPolicyReserves = errors.New("coder host: the model may not decide this gap (risky_action, missing_credential); park it as a question instead")

// ErrNoMasterDecide: the session has no live master (none, or stopped or gone) to answer, and the gap is not one that waits for the
// person, so nothing is parked.
var ErrNoMasterDecide = errors.New("coder host: no master to answer: decide and call record, or stop")

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
// the model settle it, ErrNoMasterDecide that there is no master to ask and the gap is not a hard
// one: decide, then RecordOwn.
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
	if _, has := s.hasLiveMaster(c.ProfileID, c.ConversationID); !has {
		if !HardClass(r.Class) {
			return Question{}, ErrNoMasterDecide
		}
		if s.recentPersonItems(c.ProfileID, c.ConversationID) >= MaxPersonItemsPerHour {
			return Question{}, ErrPersonItemRate
		}
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
