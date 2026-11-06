package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The boss of an open-ended run is a sibling of the chat it answers to: a top-level mission session in a conversation of its
// own, so the chat's own mission session (one per conversation) is never shadowed by it. The operator names the chat it
// stands behind in the session's client id (BossClientID); the operator's stamp (the run's charter id on the row) is what
// makes it the boss, and a client id alone is nothing.

// MaxBossAttempt is the largest attempt number of a boss key: a bigger one is no boss key, so a key an agent made with a huge number
// neither fits a client id nor blocks the next attempt.
const MaxBossAttempt = 999

// BossClientIDPrefix starts the client id of a boss session.
const BossClientIDPrefix = "boss-for-"

// BossClientID is the client id of the n-th boss session made for a front-desk chat (n from 1). Each attempt has a key of its
// own, so a run that ended leaves its boss behind and the chat's next run gets a new one, and a key somebody else took costs
// one attempt number, not the chat.
func BossClientID(frontDeskConversation string, n int) string {
	return BossClientIDPrefix + frontDeskConversation + "-" + strconv.Itoa(n)
}

// ParseBossClientID reads a boss key back: the chat and the attempt number.
func ParseBossClientID(key string) (front string, n int, ok bool) {
	rest, found := strings.CutPrefix(key, BossClientIDPrefix)
	if !found {
		return "", 0, false
	}
	i := strings.LastIndex(rest, "-")
	if i <= 0 || i == len(rest)-1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(rest[i+1:])
	if err != nil || n < 1 || n > MaxBossAttempt || strconv.Itoa(n) != rest[i+1:] {
		return "", 0, false
	}
	return rest[:i], n, true
}

// BossKeyLike is the LIKE pattern (ESCAPE '\') of every boss key made for a chat.
func BossKeyLike(frontDeskConversation string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(BossClientIDPrefix+frontDeskConversation+"-") + "%"
}

// BossKeySQL is the condition, over the column col, that holds for a boss key: the prefix and an attempt number of one to three
// digits (MaxBossAttempt is 999; TestBossKeySQLMatchesMaxBossAttempt holds the two together).
func BossKeySQL(col string) string {
	return fmt.Sprintf(`(%[1]s LIKE 'boss-for-%%' AND (%[1]s GLOB '*-[1-9]' OR %[1]s GLOB '*-[1-9][0-9]' OR %[1]s GLOB '*-[1-9][0-9][0-9]'))`, col)
}

// BossConversationSQL is a WHERE condition over ai_chat_conversations (alias given) that holds for the conversation of a
// stamped boss: a mission session the operator stamped as a run's boss that carries a boss key. The plain history list keeps
// such a conversation out; every other listing shows it marked. It starts from the few stamped sessions and is evaluated
// once per statement (an uncorrelated IN over them), not once per conversation: task_sessions has no index on its
// conversation. The IS NOT NULL keeps a session of no chat from turning a NOT IN into "unknown" for every row.
func BossConversationSQL(alias string) string {
	return fmt.Sprintf(`%s.id IN (SELECT b.conversation_id FROM task_sessions b WHERE b.doomsday_charter_id <> '' AND b.kind = 'mission'
		AND %s AND b.conversation_id IS NOT NULL)`, alias, BossKeySQL("b.client_id"))
}

// BossAnyConversationSQL is the same over any boss conversation `boss new` made, stamped or not: a top-level mission session the
// operator made under a boss key, in a conversation of its own (not the chat the key names). It is what a list of send targets
// leaves out and what the guards on a turn into a boss ask; BossFrontDesk is its Go form and the two are tested to agree.
func BossAnyConversationSQL(alias string) string {
	return fmt.Sprintf(`%s.id IN (SELECT b.conversation_id FROM task_sessions b WHERE b.kind = 'mission' AND b.created_by = 'you' AND COALESCE(b.parent_session_id, 0) = 0
		AND b.conversation_id IS NOT NULL AND %s AND b.client_id NOT LIKE 'boss-for-' || b.conversation_id || '-%%')`, alias, BossKeySQL("b.client_id"))
}

// IsBossConversation says whether the conversation is that of a stamped boss (any state): the mark the listings that show
// conversations or sessions carry.
func IsBossConversation(ctx context.Context, x Queryer, profileID, conversationID string) (bool, error) {
	var n int
	err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND doomsday_charter_id <> ''
		AND kind = 'mission' AND `+BossKeySQL("client_id"), profileID, conversationID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("tasks: looking up whether %s is a boss conversation: %w", conversationID, err)
	}
	return n > 0, nil
}

// IsBossAnyConversation says whether the conversation is one `boss new` made, stamped or not (see BossAnyConversationSQL).
func IsBossAnyConversation(ctx context.Context, x Queryer, profileID, conversationID string) (bool, error) {
	var n int
	err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_chat_conversations c WHERE c.profile_id = ? AND c.id = ? AND `+BossAnyConversationSQL("c"), profileID, conversationID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("tasks: looking up whether %s is a boss conversation: %w", conversationID, err)
	}
	return n > 0, nil
}

// OpenBossConversation is the conversation and session id of the boss that stands behind the front-desk chat while the
// profile's run is going: a mission session carrying one of the chat's keys, open, not archived, stamped with the charter and
// generation of the profile's RUNNING run. After a Stop, for a finished run's boss, or for a stamp that is not the running
// run's, there is none (empty and 0).
func OpenBossConversation(ctx context.Context, x Queryer, profileID, frontDeskConversation string) (string, int64, error) {
	rows, err := x.QueryContext(ctx, `SELECT s.client_id, s.conversation_id, s.id FROM task_sessions s
		JOIN doomsday_runs r ON r.profile = s.profile_id AND r.charter_id = s.doomsday_charter_id AND r.generation = s.doomsday_generation AND r.state = 'running'
		WHERE s.profile_id = ? AND s.client_id LIKE ? ESCAPE '\' AND s.kind = 'mission' AND s.doomsday_charter_id <> '' AND s.archived_at = ''
		AND s.state IN (?, ?, ?) ORDER BY s.id`,
		profileID, BossKeyLike(frontDeskConversation), string(SessionDraft), string(SessionRunning), string(SessionPaused))
	if err != nil {
		return "", 0, fmt.Errorf("tasks: looking for the boss of %s: %w", frontDeskConversation, err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var conv sql.NullString
		var id int64
		if err := rows.Scan(&key, &conv, &id); err != nil {
			return "", 0, err
		}
		if front, _, ok := ParseBossClientID(key); ok && front == frontDeskConversation && conv.String != "" {
			return conv.String, id, nil
		}
	}
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	return "", 0, nil
}

// BossFrontDesk says whether the conversation is the one `boss new` made for a boss session (a mission session the operator
// made under a boss key, in a conversation of its own) and, if so, the chat it stands behind and whether the operator has
// stamped it. A session an agent gave a boss key (its own chat, not the operator's) does not make an ordinary chat a boss's.
func BossFrontDesk(ctx context.Context, x Queryer, profileID, conversationID string) (front string, stamped, ok bool, err error) {
	rows, err := x.QueryContext(ctx, `SELECT client_id, doomsday_charter_id <> '' FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND kind = 'mission'
		AND created_by = 'you' AND `+BossKeySQL("client_id")+` AND COALESCE(parent_session_id, 0) = 0 ORDER BY id DESC`, profileID, conversationID)
	if err != nil {
		return "", false, false, fmt.Errorf("tasks: looking up whether %s is a boss conversation: %w", conversationID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var st bool
		if err := rows.Scan(&key, &st); err != nil {
			return "", false, false, err
		}
		if f, _, parsed := ParseBossClientID(key); parsed && f != conversationID {
			return f, st, true, nil
		}
	}
	return "", false, false, rows.Err()
}
