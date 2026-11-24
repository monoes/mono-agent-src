package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/agentroster"
)

// A boss may change the model of a session below it when it sees the model is not capable of the task. The change is bounded
// and written down: only to a model of the same runtime that the roster lists as working, never the boss's own session, only
// below the boss, only while the session still has budget, at most MaxBossModelChanges times per session, and every change is
// a row of the session's decisions ledger. It touches no budget, cap or charter line: the next turn runs under the same per-turn
// cap and the run's spend counters count it as before, so a dearer model cannot spend past what the budgets allow.

// MaxBossModelChanges bounds how often the boss may change one session's model, so it cannot flip it back and forth.
const MaxBossModelChanges = 3

const modelChangeQuestion = "change the model of this session?"

// BossModelChange is what BossSetModel did.
type BossModelChange struct {
	Session      int64  `json:"session"`
	Conversation string `json:"conversation"`
	Runtime      string `json:"runtime"`
	From         string `json:"from"`
	To           string `json:"to"`
}

// BossSetModel changes the model of the coder session below the boss's own, for its next turn. The caller must be the verified lead
// of a run that is going (as for the other boss operations).
func (s *Store) BossSetModel(ctx context.Context, sc CallerScope, profileID string, session int64, model, reason string) (BossModelChange, error) {
	model, reason = strings.TrimSpace(model), strings.TrimSpace(reason)
	if session <= 0 || model == "" || reason == "" {
		return BossModelChange{}, fmt.Errorf("%w: session, model and the reason for the change are required", ErrInvalid)
	}
	if s.doomsdayFor(ctx, profileID) == nil {
		return BossModelChange{}, ErrDoomsdayOff
	}
	sid, err := s.LeadMission(sc, profileID)
	if err != nil {
		return BossModelChange{}, err
	}
	if session == sid {
		return BossModelChange{}, fmt.Errorf("%w: you cannot change the model of your own session", ErrNotYours)
	}
	anc, err := s.ancestorsOf(profileID, session)
	if err != nil {
		return BossModelChange{}, err
	}
	if !containsNode(anc, sid) {
		return BossModelChange{}, fmt.Errorf("%w: that session is not below you", ErrNotYours)
	}
	var conv string
	err = s.db.QueryRow(`SELECT conversation_id FROM task_sessions WHERE profile_id = ? AND id = ? AND archived_at = '' AND state IN ('draft', 'running', 'paused') AND conversation_id IS NOT NULL`, profileID, session).Scan(&conv)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && conv == "") {
		return BossModelChange{}, fmt.Errorf("%w: that session is closed or has no conversation", ErrInvalid)
	}
	if err != nil {
		return BossModelChange{}, err
	}
	ch, err := s.checkModelChange(ctx, profileID, conv, model)
	if err != nil {
		return BossModelChange{}, err
	}
	ch.Session, ch.Conversation = session, conv
	res, err := s.db.Exec(`UPDATE ai_chat_conversations SET model = ?, updated_at = ? WHERE id = ? AND profile_id = ? AND runtime_id = ? AND model = ?`,
		ch.To, s.stamp(), conv, profileID, ch.Runtime, ch.From)
	if err != nil {
		return BossModelChange{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return BossModelChange{}, fmt.Errorf("%w: the session's model changed meanwhile: read it again", ErrInvalid)
	}
	if err := s.recordModelChange(profileID, sid, ch, reason); err != nil {
		return ch, fmt.Errorf("the model was changed but the ledger row could not be written: %w", err)
	}
	return ch, nil
}

// checkModelChange applies the rules that read the session and the roster, and returns the change to make.
func (s *Store) checkModelChange(ctx context.Context, profileID, conv, model string) (BossModelChange, error) {
	var ch BossModelChange
	if err := s.db.QueryRow(`SELECT runtime_id, model FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`, conv, profileID).Scan(&ch.Runtime, &ch.From); err != nil {
		return ch, err
	}
	ch.To = model
	if ch.To == ch.From {
		return ch, fmt.Errorf("%w: the session already runs %s", ErrInvalid, model)
	}
	roster, err := agentroster.List(ctx, s.db)
	if err != nil {
		return ch, fmt.Errorf("the roster could not be read, so no model is changed: %w", err)
	}
	listed := false
	for _, r := range roster {
		if r.Runtime == ch.Runtime && r.Model == model && agentroster.Works(r.Status) {
			listed = true
		}
	}
	if !listed {
		return ch, fmt.Errorf("%w: %s is not a working model of the roster for runtime %s (a model of another runtime is not allowed either)", ErrLimit, model, ch.Runtime)
	}
	budget, err := s.Budget(profileID, conv)
	if err != nil {
		return ch, err
	}
	spent, err := s.Spent(profileID, conv)
	if err != nil {
		return ch, err
	}
	if budget > 0 && spent >= budget {
		return ch, fmt.Errorf("%w: the session's budget is used up", ErrLimit)
	}
	var changes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_decisions WHERE profile_id = ? AND conversation_id = ? AND question = ?`, profileID, conv, modelChangeQuestion).Scan(&changes); err != nil {
		return ch, err
	}
	if changes >= MaxBossModelChanges {
		return ch, fmt.Errorf("%w: the model of this session was changed %d times already", ErrLimit, changes)
	}
	return ch, nil
}

func containsNode(nodes []treeNode, id int64) bool {
	for _, n := range nodes {
		if n.id == id {
			return true
		}
	}
	return false
}
