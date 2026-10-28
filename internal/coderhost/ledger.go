package coderhost

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// The decisions ledger: every decision made for a session without asking a
// person (by the model, or by a master) is written down with what else could
// have been chosen and why, so the operator can review it and revert the ones
// that can be reverted.

// Bounds: the rows hold text an agent wrote, so their size is capped, and a
// conversation keeps its newest MaxDecisionsPerChat rows (the oldest rotate out).
const (
	MaxDecisionsPerChat = 500
	maxDecisionText     = 2000
	maxAlternatives     = 10
	maxAlternativeText  = 500
	maxActorLen         = 128
	listLimitDefault    = 100
	listLimitMax        = 500
)

// Decision statuses.
const (
	DecisionActive   = "active"
	DecisionReverted = "reverted"
	// DecisionNotApplied: a default the ledger recorded, but whose note never reached the session
	// (it failed, was cancelled or expired); the question was reopened. It cannot be reverted.
	DecisionNotApplied = "not_applied"
)

// LedgerEntry is one row.
type LedgerEntry struct {
	ID             int64    `json:"id"`
	ProfileID      string   `json:"profileId"`
	ConversationID string   `json:"conversationId"`
	Class          string   `json:"class"`
	Question       string   `json:"question"`
	Choice         string   `json:"choice"`
	Alternatives   []string `json:"alternatives"`
	Reason         string   `json:"reason"`
	Reversible     bool     `json:"reversible"`
	MadeBy         string   `json:"madeBy"` // model | master
	Status         string   `json:"status"` // active | reverted
	RevertedBy     string   `json:"revertedBy,omitempty"`
	CreatedAt      string   `json:"createdAt"`
}

// LedgerFilter narrows ListDecisions. Zero values match everything.
type LedgerFilter struct {
	Class  string
	Status string
	Limit  int
}

// ErrNotReversible, ErrAlreadyReverted and ErrNoDecision say why a revert did not happen.
var (
	ErrNotReversible   = errors.New("coder host: this decision cannot be reverted")
	ErrAlreadyReverted = errors.New("coder host: this decision was already reverted")
	ErrNoDecision      = errors.New("coder host: no such decision")
)

const ledgerCols = `id, profile_id, conversation_id, class, question, choice, alternatives, reason, reversible, made_by, status, reverted_by, created_at`

// Record writes a decision. The caller fills Class, Question, Choice,
// Alternatives, Reason, Reversible and MadeBy; the text fields are cut to their
// bounds. Recording is bookkeeping: it does not carry the decision out.
//
// TRUST: Class, Reversible and MadeBy come from the caller. A model-facing caller goes through
// RecordOwn (producer.go), which fixes MadeBy, and Record itself refuses a hard class as the
// model's decision, so a model cannot have a risky action or a credential gap recorded as its own.
func (s *Store) Record(e LedgerEntry) (LedgerEntry, error) {
	if e.ProfileID == "" || e.ConversationID == "" || len(e.ConversationID) > maxConversationLen {
		return e, errors.New("coder host: a decision needs a profile and a conversation")
	}
	if !oneOf(e.Class, GapClasses) {
		return e, fmt.Errorf("coder host: %q is not a class of gap", e.Class)
	}
	if e.MadeBy != WhoModel && e.MadeBy != WhoMaster {
		return e, errors.New("coder host: a decision is made by the model or a master")
	}
	if e.Choice == "" {
		return e, errors.New("coder host: a decision needs a choice")
	}
	if e.MadeBy == WhoModel && HardClass(e.Class) {
		return e, ErrPolicyReserves
	}
	e.Question, e.Choice, e.Reason = trimTo(e.Question, maxDecisionText), trimTo(e.Choice, maxDecisionText), trimTo(e.Reason, maxDecisionText)
	alts := []string{}
	for _, a := range e.Alternatives {
		if len(alts) == maxAlternatives {
			break
		}
		alts = append(alts, trimTo(a, maxAlternativeText))
	}
	e.Alternatives = alts
	raw, _ := json.Marshal(alts)
	rev := 0
	if e.Reversible {
		rev = 1
	}
	e.Status, e.RevertedBy, e.CreatedAt = DecisionActive, "", s.stamp()
	tx, err := s.db.Begin()
	if err != nil {
		return e, err
	}
	defer tx.Rollback() //nolint:errcheck // after commit it is a no-op
	res, err := tx.Exec(`INSERT INTO coder_session_decisions (profile_id, conversation_id, class, question, choice, alternatives, reason, reversible, made_by, status, reverted_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', '', ?)`, e.ProfileID, e.ConversationID, e.Class, e.Question, e.Choice, string(raw), e.Reason, rev, e.MadeBy, e.CreatedAt)
	if err != nil {
		return e, err
	}
	e.ID, _ = res.LastInsertId()
	// Rows rotate out so one chatty session cannot grow the table without bound. The row just
	// written stays; then active reversible rows (the ones a person may still want to revert)
	// are kept in preference to active irreversible ones, and those to reverted ones; within
	// a kind the newest stay. A model-made row rotates only among the model's own rows, so a
	// model that writes rows cannot push a master's, a default's or an operator's rows out of the
	// audit trail (its own rows are then capped by MaxDecisionsPerChat on their own).
	scope := ""
	if e.MadeBy == WhoModel {
		scope = ` AND made_by = 'model'`
	}
	if _, err := tx.Exec(`DELETE FROM coder_session_decisions WHERE profile_id = ? AND conversation_id = ?`+scope+` AND id NOT IN
		(SELECT id FROM coder_session_decisions WHERE profile_id = ? AND conversation_id = ?`+scope+`
			ORDER BY (id = ?) DESC, (CASE WHEN status != 'active' THEN 0 WHEN reversible = 0 THEN 1 ELSE 2 END) DESC, id DESC LIMIT ?)`,
		e.ProfileID, e.ConversationID, e.ProfileID, e.ConversationID, e.ID, MaxDecisionsPerChat); err != nil {
		return e, err
	}
	return e, tx.Commit()
}

func scanEntry(sc interface{ Scan(...any) error }) (LedgerEntry, error) {
	var e LedgerEntry
	var alts string
	var rev int
	err := sc.Scan(&e.ID, &e.ProfileID, &e.ConversationID, &e.Class, &e.Question, &e.Choice, &alts, &e.Reason, &rev, &e.MadeBy, &e.Status, &e.RevertedBy, &e.CreatedAt)
	e.Reversible = rev != 0
	e.Alternatives = []string{}
	_ = json.Unmarshal([]byte(alts), &e.Alternatives)
	return e, err
}

// ListDecisions returns a conversation's decisions, newest first.
func (s *Store) ListDecisions(profileID, conversationID string, f LedgerFilter) ([]LedgerEntry, error) {
	q, args := `SELECT `+ledgerCols+` FROM coder_session_decisions WHERE profile_id = ? AND conversation_id = ?`, []any{profileID, conversationID}
	if f.Class != "" {
		q, args = q+` AND class = ?`, append(args, f.Class)
	}
	if f.Status != "" {
		q, args = q+` AND status = ?`, append(args, f.Status)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = listLimitDefault
	}
	if limit > listLimitMax {
		limit = listLimitMax
	}
	rows, err := s.db.Query(q+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LedgerEntry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetDecision returns one decision of a conversation.
func (s *Store) GetDecision(profileID, conversationID string, id int64) (LedgerEntry, bool, error) {
	e, err := scanEntry(s.db.QueryRow(`SELECT `+ledgerCols+` FROM coder_session_decisions WHERE profile_id = ? AND conversation_id = ? AND id = ?`, profileID, conversationID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return LedgerEntry{}, false, nil
	}
	return e, err == nil, err
}

// RevertDecision marks a decision reverted and records who did it. Only an
// active, reversible decision can be reverted. It changes the ledger and
// nothing else: telling the session (a follow-up note) is the caller's job, and
// that note must be sent by a person or with a counted host prefix such as
// "answer-"; the ledger never starts a turn.
func (s *Store) RevertDecision(profileID, conversationID string, id int64, by string) (LedgerEntry, error) {
	if by == "" || len(by) > maxActorLen {
		return LedgerEntry{}, errors.New("coder host: a revert names who reverts, at most 128 characters")
	}
	res, err := s.db.Exec(`UPDATE coder_session_decisions SET status = 'reverted', reverted_by = ?
		WHERE profile_id = ? AND conversation_id = ? AND id = ? AND status = 'active' AND reversible = 1`, by, profileID, conversationID, id)
	if err != nil {
		return LedgerEntry{}, err
	}
	e, ok, err := s.GetDecision(profileID, conversationID, id)
	if err != nil {
		return e, err
	}
	if !ok {
		return e, ErrNoDecision
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if e.Status == DecisionReverted {
			return e, ErrAlreadyReverted
		}
		return e, ErrNotReversible
	}
	return e, nil
}
