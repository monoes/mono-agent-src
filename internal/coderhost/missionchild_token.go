package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// What the launcher needs to know before it gives a mission child's turn its task token (phase P-B2 of
// docs/mastermind/plans/2026-10-27-mission-child-sessions.md). The host decides here, from the child store, which
// task a conversation is bound to; the launcher (internal/cli/chat) mints and binds the token and never takes the
// session, the task or the role from a message, a flag or an environment variable.

// ChildTask is the one task a child conversation is bound to.
type ChildTask struct {
	SessionID  int64
	TaskID     int64
	Generation int64 // identifies the attempt: a token minted for an older one is not current
}

// ErrChildNotRunnable means the conversation is a mission child whose mission no longer runs (stopped, closed,
// archived, deleted or handed to another lead). Its turn is not started: a child must never run without a task token,
// and it gets none for a mission that is over.
var ErrChildNotRunnable = errors.New("coder host: this mission child cannot run now (its mission is not running)")

// ChildTaskOf says which task the conversation is the live session of. ok is false for a conversation that has no
// live row: an ordinary session, or a child whose row has ended (it gets no token). A row that is still being
// reserved (spawning) counts, because the host can pick up the child's first message before the lead links it.
// A live row whose mission is not running, or whose lead no longer owns it, is ErrChildNotRunnable.
func (s *Store) ChildTaskOf(profileID, conversationID string) (ChildTask, bool, error) {
	if profileID == "" || conversationID == "" {
		return ChildTask{}, false, nil
	}
	rows, err := s.childrenWhere(`profile_id = ? AND state IN ('spawning', 'spawned') AND (child_conversation_id = ? OR pending_conversation_id = ?)`,
		profileID, conversationID, conversationID)
	if err != nil || len(rows) == 0 {
		return ChildTask{}, false, err
	}
	r := rows[0]
	ms, found, err := s.loadMissionSession(profileID, r.SessionID)
	if err != nil {
		return ChildTask{}, false, err
	}
	if !found || ms.archived || ms.lead != r.LeadConversationID || (ms.state != "running" && ms.state != "paused") {
		return ChildTask{}, true, ErrChildNotRunnable
	}
	return ChildTask{SessionID: r.SessionID, TaskID: r.TaskID, Generation: r.Generation}, true, nil
}

// ChildAttemptCurrent says whether the conversation is still the live session of the attempt a token was minted
// for: the row is live, names this conversation and carries the same generation. The launcher asks again after it
// has started the turn, so a token minted in the instant before the child ended or was replaced does not stand.
func (s *Store) ChildAttemptCurrent(profileID, conversationID string, t ChildTask) (bool, error) {
	return ChildAttemptCurrentIn(context.Background(), s.db, profileID, conversationID, t)
}

// ChildAttemptCurrentIn is ChildAttemptCurrent over a connection or transaction, so a launcher can ask inside the write
// transaction that mints the child's token.
func ChildAttemptCurrentIn(ctx context.Context, q interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, profileID, conversationID string, t ChildTask) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM coder_mission_children WHERE profile_id = ? AND session_id = ? AND task_id = ? AND generation = ?
		AND state IN ('spawning', 'spawned') AND (child_conversation_id = ? OR pending_conversation_id = ?)`,
		profileID, t.SessionID, t.TaskID, t.Generation, conversationID, conversationID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return n > 0, err
}

// ChildTokenRevoker ends every task token minted for a child conversation's runs. It is idempotent.
type ChildTokenRevoker interface {
	RevokeChildTokens(profileID, conversationID string) error
}

// revokeChildTokens ends the conversation's task tokens when its turn has ended or was stopped. A failure is logged;
// the token's own expiry and the launcher's revoke at the end of the run are the backstops.
func (h *Host) revokeChildTokens(profileID, conversationID string) error {
	if h.ChildTokens == nil {
		return nil
	}
	if err := h.ChildTokens.RevokeChildTokens(profileID, conversationID); err != nil {
		h.logf("coder host: revoking the task tokens of %s: %v", conversationID, err)
		return fmt.Errorf("revoking the task tokens of %s: %w", conversationID, err)
	}
	return nil
}
