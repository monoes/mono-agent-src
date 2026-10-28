package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Budget requests (phase P-F of docs/mastermind/plans/2026-10-27-mission-child-sessions.md). When a mission's
// budget is used up, its lead may ask the person for more. The request is a person item of kind budget_request:
// it waits for the person for as long as it takes (no timeout, no default, no master, no model answers it) and
// only the person's answer raises a budget.
//
// Authority:
//   - Filing is the lead's: a LeadScope from a verified session token whose process holds no task-bound binding
//     (the same check as mark and ceiling). Every other caller gets errMissionRefused.
//   - Answering (GrantBudget, DeclineBudget) takes the person's OperatorScope and nothing else. No tool, token
//     or session path reaches them; budgetreq_guard_test.go keeps every package a model can run through from
//     naming them.
//   - Every number in a request is read by the host (the target's budget and spend); the amount asked is a
//     validated number and the reason is cleaned, capped text. The person is shown numbers.
//   - A grant adds the amount to exactly the session the request names, once (the status moves from open in the
//     same transaction that raises the budget), never more than was asked, and only where the operator's own
//     limits still hold: a child's raise has to fit what its lead (and the mission's budget) can still carve.
//   - A request is open only while its mission is running or a draft, its lead and its target are not stopped, the
//     target still exists and its budget is still the one the request was filed against; this is derived from the rows each time, so a stale request can be neither answered nor
//     counted. A pending request changes nothing else: no turn starts, no task or session is touched.

// Kinds, statuses and limits of a budget request.
const (
	ItemBudgetRequest = "budget_request"

	TargetLead  = "lead"  // the lead's own session budget
	TargetChild = "child" // one child session's cap

	BudgetRequestOpen     = "open"
	BudgetRequestGranted  = "granted"
	BudgetRequestDeclined = "declined"
	BudgetRequestVoid     = "void"

	// MaxBudgetRequestUSD is the most one request may ask for.
	MaxBudgetRequestUSD = 50.0
	// MaxBudgetRequestsPerHour is how many requests one lead may file an hour, whatever became of them, so a
	// model cannot flood the person's list; MaxBudgetRequestsPerMission bounds a whole mission the same way.
	MaxBudgetRequestsPerHour    = 3
	MaxBudgetRequestsPerMission = 12

	maxBudgetReason = 200
)

// ErrBudgetRequestClosed: the request is not open (answered, or its mission or session is gone or stopped).
var ErrBudgetRequestClosed = fmt.Errorf("%w: that budget request is not open", ErrInvalid)

// BudgetRequest is a lead's request for more budget.
type BudgetRequest struct {
	ID                   int64   `json:"id"`
	ProfileID            string  `json:"profileId"`
	SessionID            int64   `json:"sessionId"`
	LeadConversationID   string  `json:"leadConversationId"`
	Target               string  `json:"target"`
	TargetConversationID string  `json:"targetConversationId"`
	TaskID               int64   `json:"taskId,omitempty"`
	AmountUSD            float64 `json:"amountUsd"`
	BudgetUSD            float64 `json:"budgetUsd"` // the target's budget when asked
	SpentUSD             float64 `json:"spentUsd"`  // what it had spent when asked
	Reason               string  `json:"reason,omitempty"`
	Status               string  `json:"status"`
	GrantedUSD           float64 `json:"grantedUsd,omitempty"`
	CreatedAt            string  `json:"createdAt"`
	AnsweredAt           string  `json:"answeredAt,omitempty"`
	Open                 bool    `json:"open"` // waiting for the person now (derived)
}

const budgetRequestColumns = `id, profile_id, session_id, lead_conversation_id, target, target_conversation_id, task_id, amount_usd, budget_usd, spent_usd, reason, status, granted_usd, created_at, answered_at`

func scanBudgetRequest(r interface{ Scan(...any) error }) (BudgetRequest, error) {
	var b BudgetRequest
	err := r.Scan(&b.ID, &b.ProfileID, &b.SessionID, &b.LeadConversationID, &b.Target, &b.TargetConversationID, &b.TaskID,
		&b.AmountUSD, &b.BudgetUSD, &b.SpentUSD, &b.Reason, &b.Status, &b.GrantedUSD, &b.CreatedAt, &b.AnsweredAt)
	return b, err
}

func validUSD(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

// RequestBudget files the lead's request. target is TargetLead (taskID ignored) or TargetChild (the task of
// one of its running child sessions). It returns the request; the person sees it as an item.
func (s *Store) RequestBudget(sc CallerScope, profileID string, sessionID int64, target string, taskID int64, amountUSD float64, reason string) (BudgetRequest, error) {
	lead, m, err := s.leadOf(sc, profileID, sessionID)
	if err != nil {
		return BudgetRequest{}, err
	}
	if err := m.openForLead(); err != nil {
		return BudgetRequest{}, err
	}
	if !validUSD(amountUSD) || amountUSD > MaxBudgetRequestUSD {
		return BudgetRequest{}, fmt.Errorf("%w: ask for an amount above 0 and at most $%.0f", ErrInvalid, MaxBudgetRequestUSD)
	}
	if s.sessionStopped(profileID, lead.Caller.ConversationID) {
		return BudgetRequest{}, fmt.Errorf("%w: the session is stopped", ErrInvalid)
	}
	b := BudgetRequest{ProfileID: profileID, SessionID: sessionID, LeadConversationID: lead.Caller.ConversationID,
		Target: target, AmountUSD: amountUSD, Reason: cleanText(reason, maxBudgetReason), Status: BudgetRequestOpen}
	switch target {
	case TargetLead:
		b.TargetConversationID = lead.Caller.ConversationID
	case TargetChild:
		c, found, err := s.Child(profileID, sessionID, taskID)
		if err != nil {
			return BudgetRequest{}, err
		}
		if !found || c.State != ChildSpawned || c.ChildConversationID == "" || c.LeadConversationID != lead.Caller.ConversationID {
			return BudgetRequest{}, fmt.Errorf("%w: that task has no running session of its own", ErrInvalid)
		}
		b.TargetConversationID, b.TaskID = c.ChildConversationID, taskID
	default:
		return BudgetRequest{}, fmt.Errorf("%w: the target is lead or child", ErrInvalid)
	}
	if b.BudgetUSD, err = s.Budget(profileID, b.TargetConversationID); err != nil || b.BudgetUSD <= 0 {
		if err == nil || errors.Is(err, sql.ErrNoRows) {
			err = fmt.Errorf("%w: that session has no budget to raise", ErrInvalid)
		}
		return BudgetRequest{}, err
	}
	if b.SpentUSD, err = s.Spent(profileID, b.TargetConversationID); err != nil {
		return BudgetRequest{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return BudgetRequest{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC()
	var perHour, perMission int
	if err := tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM coder_budget_requests WHERE profile_id = ? AND lead_conversation_id = ? AND created_at >= ?),
		(SELECT COUNT(*) FROM coder_budget_requests WHERE profile_id = ? AND session_id = ?)`,
		profileID, b.LeadConversationID, now.Add(-time.Hour).Format(timeLayout), profileID, sessionID).Scan(&perHour, &perMission); err != nil {
		return BudgetRequest{}, err
	}
	if perHour >= MaxBudgetRequestsPerHour || perMission >= MaxBudgetRequestsPerMission {
		return BudgetRequest{}, fmt.Errorf("%w: too many budget requests (at most %d an hour and %d for a mission)", ErrLimit, MaxBudgetRequestsPerHour, MaxBudgetRequestsPerMission)
	}
	// An open row that is no longer open (see budgetRequestOpen) would hold the target's one slot for good: void it.
	if old, err := s.openRowOf(tx, profileID, b.TargetConversationID); err != nil {
		return BudgetRequest{}, err
	} else if old.ID != 0 && !s.budgetRequestOpen(tx, old) {
		if _, err := tx.Exec(`UPDATE coder_budget_requests SET status = 'void', answered_at = ? WHERE id = ? AND status = 'open'`, now.Format(timeLayout), old.ID); err != nil {
			return BudgetRequest{}, err
		}
	}
	b.CreatedAt = now.Format(timeLayout)
	res, err := tx.Exec(`INSERT INTO coder_budget_requests (profile_id, session_id, lead_conversation_id, target, target_conversation_id, task_id, amount_usd, budget_usd, spent_usd, reason, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'open', ?)`,
		profileID, sessionID, b.LeadConversationID, b.Target, b.TargetConversationID, b.TaskID, b.AmountUSD, b.BudgetUSD, b.SpentUSD, b.Reason, b.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return BudgetRequest{}, fmt.Errorf("%w: a request for that session is already waiting for the person", ErrLimit)
		}
		return BudgetRequest{}, err
	}
	if b.ID, err = res.LastInsertId(); err != nil {
		return BudgetRequest{}, err
	}
	if err := addPersonItem(tx, profileID, b.LeadConversationID, ItemBudgetRequest, "", b.ID, b.CreatedAt); err != nil {
		return BudgetRequest{}, err
	}
	if err := tx.Commit(); err != nil {
		return BudgetRequest{}, err
	}
	b.Open = true
	return b, nil
}

// budgetRequestOpen says whether the person can act on b now: it is unanswered, its mission is still a draft or
// running, its lead is not stopped and its target session (and a child's row) is still there.
func (s *Store) budgetRequestOpen(q querier, b BudgetRequest) bool {
	if b.Status != BudgetRequestOpen {
		return false
	}
	m, found, err := s.loadMissionSessionIn(q, b.ProfileID, b.SessionID)
	if err != nil || !found || m.lead != b.LeadConversationID || m.openForLead() != nil {
		return false
	}
	var state string
	if err := q.QueryRow(`SELECT state FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, b.ProfileID, b.LeadConversationID).Scan(&state); err != nil || State(state) == StateStopped {
		return false
	}
	// The person decides on the numbers the request shows: once the target's budget is no longer the one it
	// was filed against (a grant, the operator, a carve returned), the request is stale and is filed again.
	var tstate string
	var tbudget float64
	if err := q.QueryRow(`SELECT state, budget_usd FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, b.ProfileID, b.TargetConversationID).Scan(&tstate, &tbudget); err != nil ||
		State(tstate) == StateStopped || math.Abs(tbudget-b.BudgetUSD) > 1e-9 {
		return false
	}
	if b.Target == TargetChild {
		c, found, err := s.childIn(q, b.ProfileID, b.SessionID, b.TaskID)
		if err != nil || !found || c.State != ChildSpawned || c.ChildConversationID != b.TargetConversationID {
			return false
		}
	}
	return true
}

// openRowOf is the target's row still marked open, or the zero request.
func (s *Store) openRowOf(q querier, profileID, target string) (BudgetRequest, error) {
	b, err := scanBudgetRequest(q.QueryRow(`SELECT `+budgetRequestColumns+` FROM coder_budget_requests WHERE profile_id = ? AND target_conversation_id = ? AND status = 'open'`, profileID, target))
	if errors.Is(err, sql.ErrNoRows) {
		return BudgetRequest{}, nil
	}
	return b, err
}

// openBudgetItems is the open-items finder of the budget_request kind.
func (s *Store) openBudgetItems(profileID string) ([]PersonItem, error) {
	rows, err := s.db.Query(`SELECT i.id, i.conversation_id, i.ref_id, i.created_at FROM coder_session_person_items i
		JOIN coder_budget_requests r ON r.id = i.ref_id AND r.profile_id = i.profile_id
		WHERE i.profile_id = ? AND i.kind = ? AND r.status = 'open' ORDER BY i.id LIMIT ?`, profileID, ItemBudgetRequest, listLimitMax)
	if err != nil {
		return nil, err
	}
	var cand []PersonItem
	for rows.Next() {
		it := PersonItem{ProfileID: profileID, Kind: ItemBudgetRequest}
		if err := rows.Scan(&it.ID, &it.ConversationID, &it.RefID, &it.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		cand = append(cand, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []PersonItem
	for _, it := range cand {
		b, err := s.GetBudgetRequest(profileID, it.RefID)
		if err == nil && b.Open {
			out = append(out, it)
		}
	}
	return out, nil
}

// GetBudgetRequest reads one request of the profile.
func (s *Store) GetBudgetRequest(profileID string, id int64) (BudgetRequest, error) {
	return s.budgetRequestIn(s.db, profileID, id)
}

func (s *Store) budgetRequestIn(q querier, profileID string, id int64) (BudgetRequest, error) {
	b, err := scanBudgetRequest(q.QueryRow(`SELECT `+budgetRequestColumns+` FROM coder_budget_requests WHERE profile_id = ? AND id = ?`, profileID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return BudgetRequest{}, fmt.Errorf("%w: no such budget request", ErrInvalid)
	}
	if err != nil {
		return BudgetRequest{}, err
	}
	b.Open = s.budgetRequestOpen(q, b)
	return b, nil
}

// ListBudgetRequests lists the profile's requests, newest first; onlyOpen keeps those waiting for the person now.
func (s *Store) ListBudgetRequests(profileID string, onlyOpen bool) ([]BudgetRequest, error) {
	rows, err := s.db.Query(`SELECT `+budgetRequestColumns+` FROM coder_budget_requests WHERE profile_id = ? ORDER BY id DESC LIMIT ?`, profileID, listLimitMax)
	if err != nil {
		return nil, err
	}
	var all []BudgetRequest
	for rows.Next() {
		b, err := scanBudgetRequest(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []BudgetRequest{}
	for _, b := range all {
		b.Open = s.budgetRequestOpen(s.db, b)
		if !onlyOpen || b.Open {
			out = append(out, b)
		}
	}
	return out, nil
}

// operatorOnly refuses everything but the person's own scope.
func operatorOnly(sc CallerScope) error {
	if _, ok := sc.(OperatorScope); !ok {
		return ErrOperatorOnly
	}
	return nil
}

// GrantBudget is the person's yes: it adds usd (0 = what was asked, never more) to the target the request names.
// It happens once: the request leaves open in the same transaction that raises the budget, and a refusal of
// the operator's limits leaves it open so the person may grant less.
func (s *Store) GrantBudget(sc CallerScope, profileID string, id int64, usd float64) (BudgetRequest, error) {
	if err := operatorOnly(sc); err != nil {
		return BudgetRequest{}, err
	}
	if usd < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return BudgetRequest{}, fmt.Errorf("%w: the amount must be a number above 0", ErrInvalid)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return BudgetRequest{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// The status moves from open in this transaction or not at all, so two answers cannot both win.
	b, err := s.budgetRequestIn(tx, profileID, id)
	if err != nil {
		return BudgetRequest{}, err
	}
	if !b.Open {
		return BudgetRequest{}, ErrBudgetRequestClosed
	}
	if usd == 0 {
		usd = b.AmountUSD
	}
	if usd > b.AmountUSD+1e-9 {
		return BudgetRequest{}, fmt.Errorf("%w: that is more than was asked ($%.2f)", ErrInvalid, b.AmountUSD)
	}
	now := s.stamp()
	res, err := tx.Exec(`UPDATE coder_budget_requests SET status = 'granted', granted_usd = ?, answered_at = ? WHERE profile_id = ? AND id = ? AND status = 'open'`, usd, now, profileID, id)
	if err != nil {
		return BudgetRequest{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return BudgetRequest{}, ErrBudgetRequestClosed
	}
	if reason, err := s.budgetRaiseRefusal(tx, b, usd); err != nil {
		return BudgetRequest{}, err
	} else if reason != "" {
		return BudgetRequest{}, fmt.Errorf("%w: %s", ErrLimit, reason)
	}
	res, err = tx.Exec(`UPDATE coder_sessions SET budget_usd = budget_usd + ?, updated_at = ? WHERE profile_id = ? AND conversation_id = ? AND budget_usd > 0`,
		usd, now, profileID, b.TargetConversationID)
	if err != nil {
		return BudgetRequest{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return BudgetRequest{}, ErrBudgetRequestClosed
	}
	if err := tx.Commit(); err != nil {
		return BudgetRequest{}, err
	}
	b.Status, b.GrantedUSD, b.AnsweredAt, b.Open = BudgetRequestGranted, usd, now, false
	return b, nil
}

// budgetRaiseRefusal says why the target's budget may not rise by usd, or "". A child's raise has to fit what its
// lead has left to carve and the mission's budget; the raise of a lead that is itself a child, what its own master
// has left. The root lead's budget is the person's to set, so a grant of what was asked is theirs alone.
func (s *Store) budgetRaiseRefusal(q querier, b BudgetRequest, usd float64) (string, error) {
	holder := "" // the session whose budget the raised cap is carved from
	if b.Target == TargetChild {
		holder = b.LeadConversationID
		if why, err := missionBudgetRefusal(q, b.ProfileID, b.SessionID, b.LeadConversationID, usd); err != nil || why != "" {
			return why, err
		}
	} else if err := q.QueryRow(`SELECT parent_conversation_id FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, b.ProfileID, b.TargetConversationID).Scan(&holder); err != nil {
		return "", err
	}
	if holder == "" {
		return "", nil
	}
	var budget, carved float64
	if err := q.QueryRow(`SELECT budget_usd, (SELECT COALESCE(SUM(budget_usd), 0) FROM coder_sessions WHERE profile_id = ? AND parent_conversation_id = ?)
		FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, b.ProfileID, holder, b.ProfileID, holder).Scan(&budget, &carved); err != nil {
		return "", err
	}
	if budget <= 0 {
		return "", nil // an uncapped holder hands out any amount
	}
	spent, err := s.spentIn(q, b.ProfileID, holder)
	if err != nil {
		return "", err
	}
	if carved+usd > budget-spent+1e-9 {
		return fmt.Sprintf("the raise would pass what the session it is carved from has left ($%.2f budget, $%.2f spent, $%.2f held by its children)", budget, spent, carved), nil
	}
	return "", nil
}

// DeclineBudget is the person's no: the request closes and nothing else changes.
func (s *Store) DeclineBudget(sc CallerScope, profileID string, id int64) (BudgetRequest, error) {
	if err := operatorOnly(sc); err != nil {
		return BudgetRequest{}, err
	}
	b, err := s.GetBudgetRequest(profileID, id)
	if err != nil {
		return BudgetRequest{}, err
	}
	now := s.stamp()
	res, err := s.db.Exec(`UPDATE coder_budget_requests SET status = 'declined', answered_at = ? WHERE profile_id = ? AND id = ? AND status = 'open'`, now, profileID, id)
	if err != nil {
		return BudgetRequest{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return BudgetRequest{}, ErrBudgetRequestClosed
	}
	b.Status, b.AnsweredAt, b.Open = BudgetRequestDeclined, now, false
	return b, nil
}
