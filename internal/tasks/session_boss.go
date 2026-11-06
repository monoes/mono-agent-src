package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The boss of an open-ended run is a sibling of the chat it answers to: a top-level mission session in a conversation of
// its own, so the chat's own mission session (one per conversation) is never shadowed by it. The operator names the
// chat it stands behind in the session's client id (BossClientID); the operator's stamp (the run's charter id on the
// row) is what makes it the boss, and a client id alone is nothing.

// BossClientIDPrefix starts the client id of a boss session.
const BossClientIDPrefix = "boss-for-"

// BossClientID is the client id of the boss session that stands behind a front-desk chat: one key per chat, so a repeated
// creation answers with the session that exists.
func BossClientID(frontDeskConversation string) string { return BossClientIDPrefix + frontDeskConversation }

// BossConversationSQL is a WHERE condition over ai_chat_conversations (alias given) that holds for the conversation of a
// stamped boss session. The history keeps such a conversation out of the chat list; reading it by id still works.
func BossConversationSQL(alias string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM task_sessions b WHERE b.profile_id = %[1]s.profile_id AND b.conversation_id = %[1]s.id AND b.doomsday_charter_id <> '')`, alias)
}

// OpenBossConversation is the conversation and session id of the stamped, open boss that stands behind the front-desk
// chat; empty and 0 when there is none.
func OpenBossConversation(ctx context.Context, x Queryer, profileID, frontDeskConversation string) (string, int64, error) {
	var conv sql.NullString
	var id int64
	err := x.QueryRowContext(ctx, `SELECT conversation_id, id FROM task_sessions WHERE profile_id = ? AND client_id = ? AND doomsday_charter_id <> ''
		AND archived_at = '' AND state IN (?, ?, ?) ORDER BY id LIMIT 1`,
		profileID, BossClientID(frontDeskConversation), string(SessionDraft), string(SessionRunning), string(SessionPaused)).Scan(&conv, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("tasks: looking for the boss of %s: %w", frontDeskConversation, err)
	}
	return conv.String, id, nil
}
