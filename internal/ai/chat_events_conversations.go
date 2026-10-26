package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// CreateConversation creates and returns a new app-level conversation
// scoped to profileID. HistoryKey is generated unconditionally (harmless
// when unused by an agent-backend conversation) so provider conversations
// always have an opaque model-context bucket distinct from workflowContext.
func (s *AIStore) CreateConversation(profileID, backend, workflowContext, runtimeID, providerID, model string) (Conversation, error) {
	return s.CreateConversationModeEffort(profileID, backend, workflowContext, runtimeID, providerID, model, ModeAssistant, "", "")
}

// CreateConversationEffort is CreateConversation with an explicit effort level.
func (s *AIStore) CreateConversationEffort(profileID, backend, workflowContext, runtimeID, providerID, model, effort string) (Conversation, error) {
	return s.CreateConversationModeEffort(profileID, backend, workflowContext, runtimeID, providerID, model, ModeAssistant, "", effort)
}

// Conversation modes.
const (
	ModeAssistant = "assistant"
	ModeCoder     = "coder"
)

// CreateConversationMode is CreateConversation with an explicit mode and
// working folder (coder conversations need one).
func (s *AIStore) CreateConversationMode(profileID, backend, workflowContext, runtimeID, providerID, model, mode, cwd string) (Conversation, error) {
	return s.CreateConversationModeEffort(profileID, backend, workflowContext, runtimeID, providerID, model, mode, cwd, "")
}

// CreateConversationModeEffort is CreateConversationMode with an explicit effort level.
func (s *AIStore) CreateConversationModeEffort(profileID, backend, workflowContext, runtimeID, providerID, model, mode, cwd, effort string) (Conversation, error) {
	switch mode {
	case ModeAssistant, ModeCoder:
	default:
		return Conversation{}, fmt.Errorf("create conversation: unknown mode %q", mode)
	}
	return s.CreateConversationAccess(profileID, backend, workflowContext, runtimeID, providerID, model, AccessForMode(mode), cwd, effort)
}

// CreateConversationAccess creates a conversation at an access level:
// scoped has no folder, every other level needs one. Mode is kept in step
// (assistant for scoped, coder otherwise) for older readers.
func (s *AIStore) CreateConversationAccess(profileID, backend, workflowContext, runtimeID, providerID, model, access, cwd, effort string) (Conversation, error) {
	if !ValidAccess(access) {
		return Conversation{}, fmt.Errorf("create conversation: unknown access %q", access)
	}
	if access == AccessScoped {
		cwd = ""
	} else if cwd == "" {
		return Conversation{}, fmt.Errorf("create conversation: access %q needs a folder", access)
	}
	if profileID == "" {
		return Conversation{}, ErrProfileRequired
	}
	now := nowRFC3339()
	c := Conversation{
		ID:              uuid.NewString(),
		ProfileID:       profileID,
		Backend:         backend,
		WorkflowContext: workflowContext,
		RuntimeID:       runtimeID,
		ProviderID:      providerID,
		Model:           model,
		Effort:          effort,
		Mode:            ModeForAccess(access),
		Access:          access,
		Cwd:             cwd,
		OrgMode:         OrgModeSolo,
		HistoryKey:      uuid.NewString(),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	const q = `INSERT INTO ai_chat_conversations
		(id, profile_id, backend, workflow_context, runtime_id, provider_id, model, session_id, mode, cwd, history_key, created_at, updated_at, effort, access)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?)`
	if _, err := s.db.Exec(q, c.ID, c.ProfileID, c.Backend, c.WorkflowContext, c.RuntimeID, c.ProviderID, c.Model, c.Mode, c.Cwd, c.HistoryKey, c.CreatedAt, c.UpdatedAt, c.Effort, c.Access); err != nil {
		return Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return c, nil
}

// GetConversation returns a conversation, scoped to profileID —
// ErrConversationNotFound both when the ID doesn't exist and when it
// belongs to a different profile, so callers cannot distinguish "wrong
// owner" from "doesn't exist".
func (s *AIStore) GetConversation(id, profileID string) (Conversation, error) {
	if profileID == "" {
		return Conversation{}, ErrProfileRequired
	}
	const q = `SELECT id, profile_id, backend, workflow_context, runtime_id, provider_id, model, session_id, mode, cwd, history_key, created_at, updated_at, effort, org_mode, access
		FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`
	var c Conversation
	err := s.db.QueryRow(q, id, profileID).Scan(
		&c.ID, &c.ProfileID, &c.Backend, &c.WorkflowContext, &c.RuntimeID, &c.ProviderID, &c.Model, &c.SessionID, &c.Mode, &c.Cwd, &c.HistoryKey, &c.CreatedAt, &c.UpdatedAt, &c.Effort, &c.OrgMode, &c.Access,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Conversation{}, ErrConversationNotFound
	}
	if err != nil {
		return Conversation{}, fmt.Errorf("get conversation: %w", err)
	}
	return c, nil
}

// ListConversations returns profileID's conversations newest-updated-first.
// cursor is the previous page's last updated_at+id (opaque to the caller);
// empty starts from the top. Returns the next cursor, or "" when exhausted.
func (s *AIStore) ListConversations(profileID, cursor string, limit int) ([]Conversation, string, error) {
	if profileID == "" {
		return nil, "", ErrProfileRequired
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args := []any{profileID}
	q := `SELECT id, profile_id, backend, workflow_context, runtime_id, provider_id, model, session_id, mode, cwd, history_key, created_at, updated_at, effort, org_mode, access
		FROM ai_chat_conversations WHERE profile_id = ?`
	if cursor != "" {
		q += ` AND updated_at || '|' || id < ?`
		args = append(args, cursor)
	}
	q += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	var out []Conversation
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.ProfileID, &c.Backend, &c.WorkflowContext, &c.RuntimeID, &c.ProviderID, &c.Model, &c.SessionID, &c.Mode, &c.Cwd, &c.HistoryKey, &c.CreatedAt, &c.UpdatedAt, &c.Effort, &c.OrgMode, &c.Access); err != nil {
			return nil, "", fmt.Errorf("scan conversation: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	nextCursor := ""
	if len(out) > limit {
		last := out[limit-1]
		nextCursor = last.UpdatedAt + "|" + last.ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}

// Org modes of a coder conversation.
const (
	OrgModeSolo    = "solo"
	OrgModeDynamic = "dynamic"
)

// SetConversationOrgMode switches a conversation between the solo and the
// dynamic org; it takes effect from the next turn.
func (s *AIStore) SetConversationOrgMode(id, profileID, mode string) error {
	if mode != OrgModeSolo && mode != OrgModeDynamic {
		return fmt.Errorf("org mode must be %q or %q, got %q", OrgModeSolo, OrgModeDynamic, mode)
	}
	if profileID == "" {
		return ErrProfileRequired
	}
	res, err := s.db.Exec(`UPDATE ai_chat_conversations SET org_mode = ?, updated_at = ? WHERE id = ? AND profile_id = ?`,
		mode, nowRFC3339(), id, profileID)
	if err != nil {
		return fmt.Errorf("set org mode: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConversationNotFound
	}
	return nil
}

// BindConversationSession records a runtime-reported resumable session id
// on session.bound. A new backend/runtime/model always creates a fresh
// conversation (see the plan's "Conversation and model context" section) —
// this only ever updates an existing conversation's own binding.
func (s *AIStore) BindConversationSession(id, profileID, runtimeID, sessionID string) error {
	if profileID == "" {
		return ErrProfileRequired
	}
	res, err := s.db.Exec(
		`UPDATE ai_chat_conversations SET runtime_id = ?, session_id = ?, updated_at = ? WHERE id = ? AND profile_id = ?`,
		runtimeID, sessionID, nowRFC3339(), id, profileID,
	)
	if err != nil {
		return fmt.Errorf("bind conversation session: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrConversationNotFound
	}
	return nil
}

// DeleteConversation removes a conversation and all its turns/events,
// scoped to profileID. Refuses while any turn is still active — a
// supervisor may be mid-write.
//
// The existence check, the active-turn check, and the delete itself all run
// inside ONE BEGIN IMMEDIATE transaction — see internal/vault/vault.go's
// Register for why IMMEDIATE, not database/sql's default DEFERRED, is
// required: a DEFERRED transaction only acquires SQLite's write lock lazily,
// at its first write, so the previous plain "check via one statement, delete
// via a later separate transaction" shape left an unprotected window between
// the two where a concurrent CreateTurn's own INSERT could commit — admitting
// an active turn this check never saw — before the delete proceeded anyway.
// BEGIN IMMEDIATE acquires the write lock up front, before the active-turn
// SELECT even runs, so for the duration of this whole function no concurrent
// writer can commit a turn for this conversation without first waiting for
// this transaction to finish (subject to the connection's busy_timeout):
// either CreateTurn's insert lands (and commits) entirely before this
// transaction begins, in which case the active-turn check below sees it and
// refuses with ErrTurnActive, or it is forced to wait until after this
// transaction has committed or rolled back — there is no interleaving in
// between where the delete can proceed past a turn it failed to see.
//
// This closes the race for any CreateTurn attempt whose insert would
// otherwise land WHILE this function's own check-then-delete was in
// progress. It does not by itself stop a CreateTurn insert that starts only
// AFTER this transaction has already committed and released the lock, from
// an admission that began racing before the delete decided anything (see the
// followups doc's "admitted in-memory but not yet DB-committed" framing) —
// that residual gap is why ai_chat_turns.conversation_id and
// ai_chat_events.conversation_id/turn_id also gained real foreign keys back
// to ai_chat_conversations (data/migrations/040_ai_chat_conversation_foreign_keys.sql):
// a CreateTurn/AppendEvent that reaches the database only after the
// conversation is already gone now fails outright instead of silently
// writing an orphan. See chat_events_test.go's
// TestChatEvents_DeleteConversationRaceAgainstConcurrentCreateTurn, which
// exercises both layers together.
func (s *AIStore) DeleteConversation(id, profileID string) error {
	if profileID == "" {
		return ErrProfileRequired
	}
	ctx := context.Background()

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("delete conversation: get conn: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin delete conversation: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	var exists int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`,
		id, profileID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check conversation exists: %w", err)
	}
	if exists == 0 {
		return ErrConversationNotFound
	}

	var activeCount int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM ai_chat_turns WHERE conversation_id = ? AND profile_id = ? AND status = ?`,
		id, profileID, turnStatusActive,
	).Scan(&activeCount); err != nil {
		return fmt.Errorf("check active turns: %w", err)
	}
	if activeCount > 0 {
		return ErrTurnActive
	}

	if _, err := conn.ExecContext(ctx, `DELETE FROM ai_chat_events WHERE conversation_id = ? AND profile_id = ?`, id, profileID); err != nil {
		return fmt.Errorf("delete events: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM ai_chat_turns WHERE conversation_id = ? AND profile_id = ?`, id, profileID); err != nil {
		return fmt.Errorf("delete turns: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`, id, profileID); err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	// What the coder host (internal/coderhost) keeps about the conversation goes with it, in the same
	// transaction: the commands still waiting for it, its session row, and its children's link to it
	// (they stay, as sessions of their own). A turn the host is running is an active turn: the check
	// above has refused the delete before this point.
	// The mission confirmations of its waiting sends (configs under the coder host's reserved
	// prefix, "coder-mission-confirm/<profile>/<conversation>/...") go with them.
	confirmPrefix := "coder-mission-confirm/" + profileID + "/" + id + "/"
	if _, err := conn.ExecContext(ctx, `DELETE FROM configs WHERE substr(name, 1, length(?)) = ?`, confirmPrefix, confirmPrefix); err != nil {
		return fmt.Errorf("delete mission confirmations: %w", err)
	}
	for _, q := range []string{
		`DELETE FROM coder_session_commands WHERE conversation_id = ? AND profile_id = ?`,
		`UPDATE coder_sessions SET role = 'solo', parent_conversation_id = '' WHERE parent_conversation_id = ? AND profile_id = ?`,
		`DELETE FROM coder_sessions WHERE conversation_id = ? AND profile_id = ?`,
	} {
		if _, err := conn.ExecContext(ctx, q, id, profileID); err != nil {
			return fmt.Errorf("delete coder session: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit delete conversation: %w", err)
	}
	committed = true
	return nil
}
