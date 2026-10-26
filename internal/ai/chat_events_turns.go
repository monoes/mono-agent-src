package ai

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// CreateTurn registers a turn, idempotent by ID: "Duplicate Start with the
// same ID returns existing admission/status, never starts a second
// process." A caller must still check the returned Turn's Status/whether it
// pre-existed via the second return value if it needs to distinguish
// "just created" from "already existed" — the row itself is identical
// either way.
func (s *AIStore) CreateTurn(conversationID, profileID, turnID, ownerInstanceID, prompt string) (turn Turn, alreadyExisted bool, err error) {
	if profileID == "" {
		return Turn{}, false, ErrProfileRequired
	}
	if existing, getErr := s.GetTurn(turnID, profileID); getErr == nil {
		return existing, true, nil
	} else if !errors.Is(getErr, ErrTurnNotFound) {
		return Turn{}, false, getErr
	}

	// Refuse a NEW turn when the conversation already has an active turn
	// owned by a different, identified live instance (see
	// ErrTurnOwnedByOtherInstance) — two live app instances sharing this
	// database must not both admit a turn on the same conversation. An
	// empty stored OwnerInstanceID (e.g. a row from before this check
	// existed) or a match against this same requesting instance is not
	// treated as foreign.
	//
	// This is a plain read-then-decide check, not wrapped in the same
	// transaction as the INSERT below: like AppendEvent's documented
	// sequence-allocation race (this same file, above), two near-
	// simultaneous CreateTurn calls from two different instances could both
	// pass this check before either inserts, admitting two active turns for
	// one conversation. That residual window is accepted here for the same
	// reason it already is there and in DeleteConversation's own,
	// similarly non-atomic active-turn check: a real fix needs a DB-level
	// constraint (e.g. a partial unique index on active turns per
	// conversation), which was deliberately NOT added — retrofitting one
	// against an already-installed database that (pre-fix) may already
	// have two active rows for one conversation would fail migration
	// outright rather than degrade gracefully. Two truly simultaneous live
	// instances racing this exact window is a narrower trigger still than
	// the already-accepted risk this closes (two live instances existing
	// at all).
	if active, getErr := s.activeTurnForConversation(conversationID, profileID); getErr == nil {
		if active.OwnerInstanceID != "" && active.OwnerInstanceID != ownerInstanceID {
			return Turn{}, false, ErrTurnOwnedByOtherInstance
		}
	} else if !errors.Is(getErr, ErrTurnNotFound) {
		return Turn{}, false, getErr
	}

	now := nowRFC3339()
	t := Turn{
		ID:              turnID,
		ConversationID:  conversationID,
		ProfileID:       profileID,
		OwnerInstanceID: ownerInstanceID,
		Prompt:          prompt,
		Status:          turnStatusActive,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	const q = `INSERT INTO ai_chat_turns (id, conversation_id, profile_id, owner_instance_id, prompt, status, reason, last_committed_seq, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, '', 0, ?, ?)`
	if _, err := s.db.Exec(q, t.ID, t.ConversationID, t.ProfileID, t.OwnerInstanceID, t.Prompt, t.Status, t.CreatedAt, t.UpdatedAt); err != nil {
		// A racing duplicate insert (two near-simultaneous Start calls with
		// the same client-generated ID) loses the INSERT but should still
		// resolve to "already existed", not an error.
		if existing, getErr := s.GetTurn(turnID, profileID); getErr == nil {
			return existing, true, nil
		}
		return Turn{}, false, fmt.Errorf("create turn: %w", err)
	}
	return t, false, nil
}

// GetTurn returns a turn scoped to profileID.
func (s *AIStore) GetTurn(turnID, profileID string) (Turn, error) {
	if profileID == "" {
		return Turn{}, ErrProfileRequired
	}
	const q = `SELECT id, conversation_id, profile_id, owner_instance_id, prompt, status, reason, last_committed_seq, created_at, updated_at
		FROM ai_chat_turns WHERE id = ? AND profile_id = ?`
	var t Turn
	err := s.db.QueryRow(q, turnID, profileID).Scan(
		&t.ID, &t.ConversationID, &t.ProfileID, &t.OwnerInstanceID, &t.Prompt, &t.Status, &t.Reason, &t.LastCommittedSeq, &t.CreatedAt, &t.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Turn{}, ErrTurnNotFound
	}
	if err != nil {
		return Turn{}, fmt.Errorf("get turn: %w", err)
	}
	return t, nil
}

// activeTurnForConversation returns conversationID's currently active turn,
// if any — CreateTurn's admission-check helper (see
// ErrTurnOwnedByOtherInstance). Returns ErrTurnNotFound when none is
// active. Deliberately unexported: it exists to answer "is this
// conversation currently claimed by a turn?", not as a general-purpose
// query — GetChatTurns/GetChatEvents callers use ListTurns/GetTurn as they
// already do. Under the normal invariant this check itself enforces, at
// most one row can be active per conversation; if that invariant were ever
// violated (e.g. by data predating this check), LIMIT 1 with the newest
// row deterministically picks one rather than erroring.
func (s *AIStore) activeTurnForConversation(conversationID, profileID string) (Turn, error) {
	if profileID == "" {
		return Turn{}, ErrProfileRequired
	}
	const q = `SELECT id, conversation_id, profile_id, owner_instance_id, prompt, status, reason, last_committed_seq, created_at, updated_at
		FROM ai_chat_turns WHERE conversation_id = ? AND profile_id = ? AND status = ? ORDER BY created_at DESC, id DESC LIMIT 1`
	var t Turn
	err := s.db.QueryRow(q, conversationID, profileID, turnStatusActive).Scan(
		&t.ID, &t.ConversationID, &t.ProfileID, &t.OwnerInstanceID, &t.Prompt, &t.Status, &t.Reason, &t.LastCommittedSeq, &t.CreatedAt, &t.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Turn{}, ErrTurnNotFound
	}
	if err != nil {
		return Turn{}, fmt.Errorf("get active turn for conversation: %w", err)
	}
	return t, nil
}

// QueryActiveTurns returns every turn across every profile and conversation
// currently left in "active" status — the startup reconciliation query
// (ReconcileActiveTurns, behind `chat history reconcile`), deliberately unscoped by profile
// since it exists to sweep the WHOLE database for a previous process's
// unfinished turns, not to serve one profile's view.
func (s *AIStore) QueryActiveTurns() ([]Turn, error) {
	const q = `SELECT id, conversation_id, profile_id, owner_instance_id, prompt, status, reason, last_committed_seq, created_at, updated_at
		FROM ai_chat_turns WHERE status = ?`
	rows, err := s.db.Query(q, turnStatusActive)
	if err != nil {
		return nil, fmt.Errorf("query active turns: %w", err)
	}
	defer rows.Close()

	var turns []Turn
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.ID, &t.ConversationID, &t.ProfileID, &t.OwnerInstanceID, &t.Prompt, &t.Status, &t.Reason, &t.LastCommittedSeq, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan active turn: %w", err)
		}
		turns = append(turns, t)
	}
	return turns, rows.Err()
}

// ListTurns returns a conversation's turns newest-first, scoped to
// profileID.
func (s *AIStore) ListTurns(conversationID, profileID, cursor string, limit int) ([]Turn, string, error) {
	if profileID == "" {
		return nil, "", ErrProfileRequired
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args := []any{conversationID, profileID}
	q := `SELECT id, conversation_id, profile_id, owner_instance_id, prompt, status, reason, last_committed_seq, created_at, updated_at
		FROM ai_chat_turns WHERE conversation_id = ? AND profile_id = ?`
	if cursor != "" {
		q += ` AND created_at || '|' || id < ?`
		args = append(args, cursor)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list turns: %w", err)
	}
	defer rows.Close()

	var out []Turn
	for rows.Next() {
		var t Turn
		if err := rows.Scan(&t.ID, &t.ConversationID, &t.ProfileID, &t.OwnerInstanceID, &t.Prompt, &t.Status, &t.Reason, &t.LastCommittedSeq, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, "", fmt.Errorf("scan turn: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	nextCursor := ""
	if len(out) > limit {
		last := out[limit-1]
		nextCursor = last.CreatedAt + "|" + last.ID
		out = out[:limit]
	}
	return out, nextCursor, nil
}

// AppendEvent allocates the next sequence number for (conversationID,
// turnID), inserts the event, and advances the turn's last_committed_seq —
// all in one transaction, committed before the caller may emit the
// equivalent live Wails event ("commit before emitting the identical
// event").
//
// This does NOT by itself make the MAX(seq)+1 read-then-insert race-free:
// modernc.org/sqlite opens plain "begin" (deferred) transactions unless the
// DSN sets _txlock=immediate (it doesn't, and changing that is a
// connection-wide behavior change out of scope here), so two concurrent
// AppendEvent calls on the same turn can both read the same MAX(seq) before
// either writes — the PRIMARY KEY then fails the second INSERT (or SQLite
// returns "database is locked") rather than silently corrupting data, but
// it is a hard failure, not a clean serialization. Callers must not invoke
// AppendEvent concurrently for the same (profileID, conversationID, turnID);
// the turn's journal (cmd/monoagentcli) serializes writes per turn (single
// active writer per turn), matching this store's transactional guarantees.
func (s *AIStore) AppendEvent(profileID, conversationID, turnID string, typ chatevents.EventType, payload any) (chatevents.Event, error) {
	if profileID == "" {
		return chatevents.Event{}, ErrProfileRequired
	}
	tx, err := s.db.Begin()
	if err != nil {
		return chatevents.Event{}, fmt.Errorf("begin append event: %w", err)
	}
	defer tx.Rollback()

	ev, err := appendEventTx(tx, profileID, conversationID, turnID, typ, payload, time.Now())
	if err != nil {
		return chatevents.Event{}, err
	}
	if _, err := tx.Exec(
		`UPDATE ai_chat_turns SET last_committed_seq = ?, updated_at = ? WHERE id = ? AND profile_id = ?`,
		ev.Seq, nowRFC3339(), turnID, profileID,
	); err != nil {
		return chatevents.Event{}, fmt.Errorf("advance last_committed_seq: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return chatevents.Event{}, fmt.Errorf("commit append event: %w", err)
	}
	return ev, nil
}

// appendEventTx does the sequence-allocation-and-insert step within an
// already-open transaction, shared by AppendEvent and FinalizeTurn (which
// needs its terminal-event insert in the SAME transaction as its
// compare-and-set status update).
func appendEventTx(tx *sql.Tx, profileID, conversationID, turnID string, typ chatevents.EventType, payload any, at time.Time) (chatevents.Event, error) {
	var maxSeq sql.NullInt64
	if err := tx.QueryRow(
		`SELECT MAX(seq) FROM ai_chat_events WHERE profile_id = ? AND conversation_id = ? AND turn_id = ?`,
		profileID, conversationID, turnID,
	).Scan(&maxSeq); err != nil {
		return chatevents.Event{}, fmt.Errorf("allocate sequence: %w", err)
	}
	seq := maxSeq.Int64 + 1

	ev, err := chatevents.New(profileID, conversationID, turnID, seq, at, typ, payload)
	if err != nil {
		return chatevents.Event{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO ai_chat_events (profile_id, conversation_id, turn_id, seq, created_at, version, type, payload) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		profileID, conversationID, turnID, ev.Seq, ev.At, ev.Version, string(ev.Type), string(ev.Payload),
	); err != nil {
		return chatevents.Event{}, fmt.Errorf("insert event: %w", err)
	}
	return ev, nil
}

// GetEvents returns a turn's events with seq > afterSeq, ascending — the
// shape both initial hydration (afterSeq=0) and gap catch-up (afterSeq=last
// known) use.
func (s *AIStore) GetEvents(conversationID, turnID, profileID string, afterSeq int64, limit int) ([]chatevents.Event, error) {
	if profileID == "" {
		return nil, ErrProfileRequired
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	const q = `SELECT profile_id, conversation_id, turn_id, seq, created_at, version, type, payload
		FROM ai_chat_events WHERE profile_id = ? AND conversation_id = ? AND turn_id = ? AND seq > ?
		ORDER BY seq ASC LIMIT ?`
	rows, err := s.db.Query(q, profileID, conversationID, turnID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}
	defer rows.Close()

	var out []chatevents.Event
	for rows.Next() {
		var ev chatevents.Event
		var payload string
		var typ string
		if err := rows.Scan(&ev.ProfileID, &ev.ConversationID, &ev.TurnID, &ev.Seq, &ev.At, &ev.Version, &typ, &payload); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		ev.Type = chatevents.EventType(typ)
		ev.Payload = []byte(payload)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// FinalizeTurn transitions a turn from active to a terminal status and
// inserts its turn.finished event in one transaction, compare-and-set from
// "active" so a duplicate finalize (e.g. a race between EOF handling and a
// stale Stop) is a harmless no-op rather than a double terminal event —
// "exactly once after supervisor completion". Returns alreadyFinalized=true
// when the compare-and-set found the turn already in a terminal state.
// FinalizeTurn returns the committed turn.finished event so the caller (the
// turn's journal) can print the identical event live after this call commits
// — "commit before emit" — without a second read. ev is the zero value when
// alreadyFinalized is true (nothing was written this call) or err != nil.
func (s *AIStore) FinalizeTurn(profileID, conversationID, turnID string, status chatevents.TurnStatus, reason string, exitCode *int, historySaved bool) (ev chatevents.Event, alreadyFinalized bool, err error) {
	return s.FinalizeTurnCode(profileID, conversationID, turnID, status, reason, "", exitCode, historySaved, "")
}

// FinalizeTurnCode is FinalizeTurn with a failure code and the turn's
// sandbox for the turn.finished payload (chatevents.TurnFinishedPayload's
// Code and Sandbox).
func (s *AIStore) FinalizeTurnCode(profileID, conversationID, turnID string, status chatevents.TurnStatus, reason, code string, exitCode *int, historySaved bool, sandbox string) (ev chatevents.Event, alreadyFinalized bool, err error) {
	if profileID == "" {
		return chatevents.Event{}, false, ErrProfileRequired
	}
	tx, err := s.db.Begin()
	if err != nil {
		return chatevents.Event{}, false, fmt.Errorf("begin finalize turn: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`UPDATE ai_chat_turns SET status = ?, reason = ?, updated_at = ? WHERE id = ? AND profile_id = ? AND status = ?`,
		string(status), reason, nowRFC3339(), turnID, profileID, turnStatusActive,
	)
	if err != nil {
		return chatevents.Event{}, false, fmt.Errorf("compare-and-set turn status: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return chatevents.Event{}, true, tx.Commit() // already terminal; nothing to do, not an error
	}

	ev, err = appendEventTx(tx, profileID, conversationID, turnID, chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{
		Status:       status,
		Reason:       reason,
		Code:         code,
		ExitCode:     exitCode,
		HistorySaved: historySaved,
		Sandbox:      sandbox,
	}, time.Now())
	if err != nil {
		return chatevents.Event{}, false, err
	}
	if _, err := tx.Exec(
		`UPDATE ai_chat_turns SET last_committed_seq = ? WHERE id = ? AND profile_id = ?`,
		ev.Seq, turnID, profileID,
	); err != nil {
		return chatevents.Event{}, false, fmt.Errorf("advance last_committed_seq on finalize: %w", err)
	}
	return ev, false, tx.Commit()
}

// ReconciledReason is the reason ReconcileActiveTurns records on a turn it
// marks interrupted.
const ReconciledReason = "interrupted at backend startup (previous app instance)"

// ReconcileActiveTurns marks every turn still "active", across every
// profile, as interrupted — the sweep a starting app runs over turns a
// previous instance left unfinished. A turn owned by exceptOwner (the
// starting instance itself, which may already have admitted a turn by the
// time the sweep runs) is skipped; an empty exceptOwner skips nothing.
// It cannot tell a dead previous instance from a second live one, since no
// liveness signal exists. Per-row failures are collected, not fatal.
func (s *AIStore) ReconcileActiveTurns(exceptOwner string) (reconciled []Turn, errs []error) {
	return s.ReconcileActiveTurnsSkipping(exceptOwner, nil)
}

// ReconcileActiveTurnsSkipping is ReconcileActiveTurns that also leaves alone
// the turns skip reports true for: turns whose process is known to be alive
// somewhere else (the coder host's), which a starting app must not interrupt.
func (s *AIStore) ReconcileActiveTurnsSkipping(exceptOwner string, skip func(Turn) bool) (reconciled []Turn, errs []error) {
	rows, err := s.QueryActiveTurns()
	if err != nil {
		return nil, []error{fmt.Errorf("list active turns: %w", err)}
	}
	reconciled = []Turn{}
	for _, row := range rows {
		if exceptOwner != "" && row.OwnerInstanceID == exceptOwner {
			continue
		}
		if skip != nil && skip(row) {
			continue
		}
		_, already, err := s.FinalizeTurn(row.ProfileID, row.ConversationID, row.ID, chatevents.StatusInterrupted, ReconciledReason, nil, true)
		if err != nil {
			errs = append(errs, fmt.Errorf("reconcile turn %s: %w", row.ID, err))
			continue
		}
		if !already {
			row.Status = string(chatevents.StatusInterrupted)
			row.Reason = ReconciledReason
			reconciled = append(reconciled, row)
		}
	}
	return reconciled, errs
}
