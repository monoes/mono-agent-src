package ai

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/coderspend"
	"github.com/monoes/mono-agent/internal/procbind"
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
	return s.deleteConversationInTx(id, profileID, nil)
}

// ErrConversationGuarded is returned by DeleteConversationIfUnowned when the guard refused: nothing was deleted.
var ErrConversationGuarded = errors.New("chat: the conversation is not the caller's to delete")

// DeleteConversationIfUnowned is DeleteConversation with a guard that runs on the delete's own connection, inside
// its BEGIN IMMEDIATE transaction and after the existence check, so what the guard saw cannot change before the
// delete commits. The guard returns false to refuse (ErrConversationGuarded, nothing deleted); an error aborts.
func (s *AIStore) DeleteConversationIfUnowned(id, profileID string, mayDelete func(ctx context.Context, conn *sql.Conn) (bool, error)) error {
	return s.deleteConversationInTx(id, profileID, mayDelete)
}

// DeleteConversationGuarded is DeleteConversation with a last check made inside the write transaction
// (a guard error rolls the delete back and is returned wrapped, so errors.Is still finds it). It runs on
// the same helper as DeleteConversationIfUnowned: the guard runs after the existence check and BEFORE
// the active-turn check. It is how a caller that has to undo something outside the database first
// (stop the processes the conversation's turns left running) learns that the record it acted on is
// still the whole record.
func (s *AIStore) DeleteConversationGuarded(id, profileID string, guard func(ctx context.Context, conn *sql.Conn) error) error {
	if guard == nil {
		return s.deleteConversationInTx(id, profileID, nil)
	}
	return s.deleteConversationInTx(id, profileID, func(ctx context.Context, conn *sql.Conn) (bool, error) {
		return true, guard(ctx, conn)
	})
}

// ConversationOwner is the owner of the mission tokens the runs of a conversation mint (dynorg.ConversationOwner,
// which this package cannot import; internal/cli/chat pins the two equal in a test).
func ConversationOwner(profileID, conversationID string) string {
	h := sha256.Sum256([]byte(profileID + "\x00" + conversationID))
	return "conv-" + hex.EncodeToString(h[:8])
}

// deleteConversationInTx is the body of DeleteConversation; guard, when not nil, runs in the transaction first.
func (s *AIStore) deleteConversationInTx(id, profileID string, guard func(ctx context.Context, conn *sql.Conn) (bool, error)) error {
	if profileID == "" {
		return ErrProfileRequired
	}
	ctx := context.Background()

	// The row keys of the v2 tokens are derived before the write transaction (deriving may write).
	keyFor, err := procbind.Revoker{DB: s.db}.Keys(ctx, []procbind.Kind{procbind.KindSession, procbind.KindMission}, procbind.Scope{Profile: profileID, Session: id, Owner: ConversationOwner(profileID, id)})
	if err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}

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
	if guard != nil {
		ok, err := guard(ctx, conn)
		if err != nil {
			return fmt.Errorf("delete conversation: guard: %w", err)
		}
		if !ok {
			return ErrConversationGuarded
		}
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
	// A mission child's spend is written down before the usage it is computed from goes: unconditionally, so no
	// caller (or older binary) can delete a child without it. A failure aborts the delete.
	if err := coderspend.Snapshot(ctx, conn, profileID, id); err != nil {
		return fmt.Errorf("record the child's spend: %w", err)
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
		// A scoped agent token bound to a task of this conversation ends with it.
		// procbind:legacy-only a bound API token has no v2 form: the v2 families are mission and session tokens.
		`UPDATE agent_tokens SET revoked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE bound_conversation = ?1 AND profile_id = ?2 AND revoked_at IS NULL`,
		`DELETE FROM coder_session_commands WHERE conversation_id = ? AND profile_id = ?`,
		`DELETE FROM coder_session_decisions WHERE conversation_id = ? AND profile_id = ?`,
		`DELETE FROM coder_session_questions WHERE conversation_id = ? AND profile_id = ?`,
		`DELETE FROM coder_session_person_items WHERE conversation_id = ? AND profile_id = ?`,
		`DELETE FROM coder_session_turn_caps WHERE conversation_id = ? AND profile_id = ?`,
		`DELETE FROM coder_session_tokens WHERE conversation_id = ? AND profile_id = ?`,
		`DELETE FROM coder_session_host_turns WHERE conversation_id = ? AND profile_id = ?`,
		// Its AI task's token carries the conversation's privileges: it stops working, and a task still open is cancelled.
		`UPDATE ai_tasks SET token_hash = '', holder_pid = 0, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now'),
			status = CASE WHEN status IN ('queued', 'running', 'needs_user') THEN 'cancelled' ELSE status END,
			error = CASE WHEN status IN ('queued', 'running', 'needs_user') THEN 'the conversation was deleted' ELSE error END
			WHERE conversation_id = ?1 AND profile_id = ?2`,
		// A task a child session was running (or was about to run: its conversation is still pending) goes to "gone" (its lead learns that from the row); what the
		// conversation led as a mission lead (audit rows, marks, policy) goes with it.
		`UPDATE coder_mission_children SET state = 'gone', end_reason = 'deleted', reserved_usd = 0, ended_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE (child_conversation_id = ?1 OR pending_conversation_id = ?1) AND profile_id = ?2 AND state IN ('spawning', 'spawned')`,
		`DELETE FROM coder_mission_events WHERE profile_id = ?2 AND session_id IN (SELECT session_id FROM coder_mission_policy WHERE lead_conversation_id = ?1 AND profile_id = ?2)`,
		`DELETE FROM coder_mission_children WHERE lead_conversation_id = ?1 AND profile_id = ?2`,
		`DELETE FROM coder_mission_policy WHERE lead_conversation_id = ?1 AND profile_id = ?2`,
		// A budget request the conversation made as a lead goes with it; one that named it as the target (a child's
		// cap) closes (void) and stays as the lead's record, so it can never be granted.
		`DELETE FROM coder_budget_requests WHERE lead_conversation_id = ?1 AND profile_id = ?2`,
		`UPDATE coder_budget_requests SET status = 'void', answered_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE target_conversation_id = ?1 AND profile_id = ?2 AND status = 'open'`,
		// The spawn advisory's remembered refusal belongs to the lead that was refused.
		`DELETE FROM coder_spawn_advice WHERE spawner = ?1 AND profile_id = ?2`,
		`UPDATE coder_sessions SET role = 'solo', parent_conversation_id = '' WHERE parent_conversation_id = ? AND profile_id = ?`,
		`DELETE FROM coder_sessions WHERE conversation_id = ? AND profile_id = ?`,
		// The answers its questions were given.
		`DELETE FROM ai_chat_answers WHERE conversation_id = ? AND profile_id = ?`,
	} {
		if _, err := conn.ExecContext(ctx, q, id, profileID); err != nil {
			return fmt.Errorf("delete coder session: %w", err)
		}
	}
	// The conversation's v2 tokens are revoked in this same transaction, whichever form of delete runs it: the
	// session tokens of the conversation and the mission tokens its runs minted (the owner, with or without a run
	// suffix). If that cannot be written the delete is rolled back (decision K1), so a delete never leaves a live
	// v2 token behind.
	var refs []procbind.TokenRef
	for _, sel := range []procbind.Selector{
		{Profile: profileID, Session: id, Kind: procbind.KindSession},
		{Profile: profileID, Owner: ConversationOwner(profileID, id), Kind: procbind.KindMission},
	} {
		got, err := procbind.RevokeTx(ctx, conn, keyFor, sel, "conversation deleted", time.Now())
		if err != nil {
			return fmt.Errorf("revoke the v2 tokens: %w", err)
		}
		refs = append(refs, got...)
	}
	// Its spawn log goes with it as a parent. A deleted CHILD's row stays: removing it would hand its
	// master the rate slot back, so spawn-and-delete could outrun the spawn rate limit (the row ages out).
	if _, err := conn.ExecContext(ctx, `DELETE FROM coder_session_spawns WHERE parent = ? AND profile_id = ?`, id, profileID); err != nil {
		return fmt.Errorf("delete coder session: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit delete conversation: %w", err)
	}
	committed = true
	procbind.Drop(s.binder, refs)
	return nil
}
