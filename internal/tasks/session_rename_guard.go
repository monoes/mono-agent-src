package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const createdChatPrefix = "chat "

// createdNote is the note of a session's "created" event: the chat an agent started it from. A name
// is a claim anyone can repeat (--as); the chat comes from a verified token, so it is what later tells
// the creator from an agent that only uses its name.
func createdNote(actor Actor) string {
	if actor.Kind == Agent && actor.Conversation != "" {
		return createdChatPrefix + actor.Conversation
	}
	return ""
}

// checkRenameOwner holds an agent's rename to the session's creator: it was started under this name,
// from a chat the start recorded, the caller proves that same chat, and it is still a draft. A draft
// started with no chat has nothing to prove the creator by (a name is a label), so the operator renames
// it. The operator is not asked.
func (s *Store) checkRenameOwner(ctx context.Context, x dbx, se Session, actor Actor) error {
	if actor.Kind != Agent {
		return nil
	}
	if se.CreatedBy != actor.Label() {
		return operatorOnly(fmt.Sprintf("rename session #%d: it was started by %s, not by this agent", se.ID, se.CreatedBy))
	}
	var note string
	err := x.QueryRowContext(ctx, `SELECT note FROM task_session_events WHERE session_id = ? AND kind = 'created' ORDER BY id LIMIT 1`, se.ID).Scan(&note)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("tasks: reading the start of session #%d: %w", se.ID, err)
	}
	if note == "" {
		return operatorOnly(fmt.Sprintf("rename session #%d: it was started with no chat to tell its creator by; only the operator renames it", se.ID))
	}
	if strings.TrimPrefix(note, createdChatPrefix) != actor.Conversation {
		return operatorOnly(fmt.Sprintf("rename session #%d: it was started from another chat than this agent's", se.ID))
	}
	if se.State != SessionDraft {
		return operatorOnly(fmt.Sprintf("rename session #%d: it is %s; an agent renames a draft only", se.ID, se.State))
	}
	return nil
}
