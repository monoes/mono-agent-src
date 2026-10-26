package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// Task sessions (docs/mastermind/plans/2026-10-25-coder-missions-task-sessions.md).
//
// The trust rule they keep: the operator opens a session, not each task. Until the
// operator approves the plan the session is a draft, whose tasks nobody can take;
// approval is the operator's alone. After it the agents work inside the session and
// nowhere else, and what only the operator may do (approve, resume, budget, caps,
// access level, auto-close, archive, closing a task) stays hers.

const sessionCols = `id, profile_id, conversation_id, title, goal, state, access_level, budget_usd, spent_usd, max_tasks, max_depth,
	max_retries, auto_close_verified, created_by, approved_by, approved_at, archived_at, created_at, updated_at, client_id`

func (s *Store) scanSession(row rowScanner) (Session, error) {
	var se Session
	var conv, cid sql.NullString
	var state, approvedAt, archivedAt, created, updated string
	var auto int
	if err := row.Scan(&se.ID, &se.ProfileID, &conv, &se.Title, &se.Goal, &state, &se.AccessLevel, &se.BudgetUSD, &se.SpentUSD,
		&se.MaxTasks, &se.MaxDepth, &se.MaxRetries, &auto, &se.CreatedBy, &se.ApprovedBy, &approvedAt, &archivedAt, &created, &updated, &cid); err != nil {
		return Session{}, err
	}
	se.ClientID = cid.String
	se.ConversationID, se.State, se.AutoCloseVerified, se.Archived = conv.String, SessionState(state), auto == 1, archivedAt != ""
	var err error
	if approvedAt != "" {
		t, err := storedTime("approved_at", approvedAt)
		if err != nil {
			return Session{}, err
		}
		se.ApprovedAt = &t
	}
	if se.CreatedAt, err = storedTime("created_at", created); err != nil {
		return Session{}, err
	}
	if se.UpdatedAt, err = storedTime("updated_at", updated); err != nil {
		return Session{}, err
	}
	return se, nil
}

// sessionTx reads one session of the profile, with its revision and counts.
func (s *Store) sessionTx(ctx context.Context, x dbx, profileID string, id int64) (Session, error) {
	se, err := s.scanSession(x.QueryRowContext(ctx, `SELECT `+sessionCols+` FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, sessionNotFound(id)
	}
	if err != nil {
		return Session{}, fmt.Errorf("tasks: reading session #%d: %w", id, err)
	}
	return s.fillSession(ctx, x, se)
}

func (s *Store) fillSession(ctx context.Context, x dbx, se Session) (Session, error) {
	if err := x.QueryRowContext(ctx, `SELECT COALESCE((SELECT rev FROM task_session_rev WHERE session_id = ?), 0)`, se.ID).Scan(&se.Rev); err != nil {
		return Session{}, fmt.Errorf("tasks: reading the session revision: %w", err)
	}
	c, err := s.sessionCounts(ctx, x, se.ID)
	if err != nil {
		return Session{}, err
	}
	se.Counts = c
	return se, nil
}

// sessionCounts counts a session's tasks per column; archived ones and epics are not counted
// (epics are counted apart in Epics).
func (s *Store) sessionCounts(ctx context.Context, x dbx, id int64) (SessionCounts, error) {
	var c SessionCounts
	rows, err := x.QueryContext(ctx,
		`SELECT status, blocked, verified, kind = 'epic', COUNT(*) FROM tasks WHERE session_id = ? AND status <> 'archived' GROUP BY status, blocked, verified, kind = 'epic'`, id)
	if err != nil {
		return c, fmt.Errorf("tasks: counting a session: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var blocked, verified, epic, n int
		if err := rows.Scan(&st, &blocked, &verified, &epic, &n); err != nil {
			return c, fmt.Errorf("tasks: counting a session: %w", err)
		}
		if epic == 1 { // an epic only groups: it is never done, so it is not progress
			c.Epics += n
			continue
		}
		switch Status(st) {
		case StatusInbox:
			c.Inbox += n
		case StatusReady:
			c.Ready += n
		case StatusInProgress:
			c.InProgress += n
		case StatusReview:
			c.Review += n
		case StatusDone:
			c.Done += n
		}
		c.Total += n
		if blocked == 1 {
			c.Blocked += n
		}
		if verified == 1 {
			c.Verified += n
		}
	}
	if err := rows.Err(); err != nil {
		return c, fmt.Errorf("tasks: counting a session: %w", err)
	}
	return c, nil
}

// sessionEvent writes one row of a session's history.
func (s *Store) sessionEvent(ctx context.Context, x dbx, id int64, actor Actor, kind, note string) error {
	_, err := x.ExecContext(ctx, `INSERT INTO task_session_events (session_id, at, actor, kind, note) VALUES (?,?,?,?,?)`,
		id, s.stamp(), actor.Label(), kind, cutBytes(cleanText(note), MaxCommentBytes))
	if err != nil {
		return fmt.Errorf("tasks: writing a session event: %w", err)
	}
	return nil
}

// touchSession stamps the session's update time and moves its revision.
func (s *Store) touchSession(ctx context.Context, x dbx, id int64) error {
	if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET updated_at = ? WHERE id = ?`, s.stamp(), id); err != nil {
		return fmt.Errorf("tasks: updating session #%d: %w", id, err)
	}
	return s.bumpSession(ctx, x, id)
}

// sessionWrite runs fn on session id in one write transaction and returns the session as
// it is afterwards. fn sees the session as it was; the revision moves once.
func (s *Store) sessionWrite(ctx context.Context, profileID string, id int64, fn func(x dbx, se Session) error) (Session, error) {
	var out Session
	err := s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		se, err := s.sessionTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if err := fn(x, se); err != nil {
			return err
		}
		if err := s.touchSession(ctx, x, id); err != nil {
			return err
		}
		out, err = s.sessionTx(ctx, x, profileID, id)
		return err
	})
	if err != nil {
		return Session{}, err
	}
	return out, nil
}

// readerOf judges who reads sessions: the operator and agents do, a capture does not, and
// an agent bound to a session reads only that one (the others are not found).
func readerOf(actor Actor, session int64) error {
	switch actor.Kind {
	case Human, Agent:
	case Capture:
		return operatorOnly("read sessions")
	default:
		return invalid("unknown actor")
	}
	if session != 0 && actor.Session != 0 && actor.Session != session {
		return sessionNotFound(session)
	}
	return nil
}

func validAccess(level string) bool { return slices.Contains(AccessLevels, level) }

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// CreateSession starts a session, in the draft state. The operator and a named agent
// may; an agent that plans cannot set the budget, the caps or the access level (the
// operator does, before or after she approves), and an agent bound to a session cannot
// start another. A profile holds at most MaxOpenSessions sessions that are not archived.
func (s *Store) CreateSession(ctx context.Context, profileID string, in SessionInput, actor Actor) (Session, error) {
	switch actor.Kind {
	case Human:
	case Agent:
		if err := needName(actor); err != nil {
			return Session{}, err
		}
		if actor.Session != 0 {
			return Session{}, operatorOnly("start a session: this agent is bound to session #" + fmt.Sprint(actor.Session))
		}
		if actor.Task != 0 || actor.Verifier {
			return Session{}, operatorOnly("start a session: this agent is bound to a task")
		}
		if in.AccessLevel != "" || in.BudgetUSD != 0 || in.MaxTasks != 0 || in.MaxDepth != 0 {
			return Session{}, operatorOnly("set a session's access level, budget or caps")
		}
	default:
		return Session{}, operatorOnly("start a session")
	}
	title := cutRunes(oneLine(in.Title), MaxTitleRunes)
	if title == "" {
		return Session{}, invalid("a session needs a title")
	}
	goal := cutBytes(cleanText(in.Goal), MaxGoalBytes)
	level := in.AccessLevel
	if level == "" {
		level = DefaultAccessLevel
	}
	if !validAccess(level) {
		return Session{}, invalid("unknown access level %q (use %s)", echo(level), strings.Join(AccessLevels, ", "))
	}
	if !finite(in.BudgetUSD) || in.BudgetUSD < 0 {
		return Session{}, invalid("the budget is an amount in dollars, 0 or more")
	}
	maxTasks, maxDepth := in.MaxTasks, in.MaxDepth
	if maxTasks == 0 {
		maxTasks = DefaultSessionMaxTasks
	}
	if maxDepth == 0 {
		maxDepth = DefaultSessionMaxDepth
	}
	if err := checkCaps(maxTasks, maxDepth, DefaultSessionMaxRetries); err != nil {
		return Session{}, err
	}
	var conv any
	if c := strings.TrimSpace(in.ConversationID); c != "" {
		conv = cutRunes(c, MaxNameLen*2)
	}
	clientID := strings.TrimSpace(in.ClientID)
	if clientID != "" && !clientIDRE.MatchString(clientID) {
		return Session{}, invalid("client id must be 1 to %d letters, digits, - or _", MaxClientIDLen)
	}
	var out Session
	err := s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		if c, ok := conv.(string); ok && actor.Kind == Agent {
			var n int
			if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`, c, profileID).Scan(&n); err != nil {
				return fmt.Errorf("tasks: looking up the conversation: %w", err)
			}
			if n == 0 {
				return invalid("conversation %q is not a chat of this profile: an agent links a session only to its own chat", echo(c))
			}
		}
		if clientID != "" {
			var existing int64
			err := x.QueryRowContext(ctx, `SELECT id FROM task_sessions WHERE profile_id = ? AND client_id = ?`, profileID, clientID).Scan(&existing)
			if err == nil {
				out, err = s.sessionTx(ctx, x, profileID, existing)
				out.Deduped = true
				return err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("tasks: looking up a session by client id: %w", err)
			}
		}
		var open int
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sessions WHERE profile_id = ? AND archived_at = ''`, profileID).Scan(&open); err != nil {
			return fmt.Errorf("tasks: counting sessions: %w", err)
		}
		if open >= MaxOpenSessions {
			return limit("this profile already has %d sessions that are not archived: archive some first", MaxOpenSessions)
		}
		stamp := s.stamp()
		var cid any
		if clientID != "" {
			cid = clientID
		}
		res, err := x.ExecContext(ctx,
			`INSERT INTO task_sessions (profile_id, conversation_id, title, goal, access_level, budget_usd, max_tasks, max_depth, max_retries, created_by, created_at, updated_at, client_id)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			profileID, conv, title, goal, level, in.BudgetUSD, maxTasks, maxDepth, DefaultSessionMaxRetries, actor.Label(), stamp, stamp, cid)
		if err != nil {
			return fmt.Errorf("tasks: adding a session: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("tasks: adding a session: %w", err)
		}
		if err := s.sessionEvent(ctx, x, id, actor, "created", ""); err != nil {
			return err
		}
		if err := s.bumpSession(ctx, x, id); err != nil {
			return err
		}
		out, err = s.sessionTx(ctx, x, profileID, id)
		return err
	})
	if err != nil {
		return Session{}, err
	}
	return out, nil
}

func checkCaps(maxTasks, maxDepth, maxRetries int) error {
	switch {
	case maxTasks < 1 || maxTasks > MaxSessionTasksCeiling:
		return invalid("max tasks is 1 to %d", MaxSessionTasksCeiling)
	case maxDepth < 1 || maxDepth > MaxSessionDepthCeiling:
		return invalid("max depth is 1 to %d", MaxSessionDepthCeiling)
	case maxRetries < 0 || maxRetries > MaxSessionRetriesCeiling:
		return invalid("max retries is 0 to %d", MaxSessionRetriesCeiling)
	}
	return nil
}

// GetSession returns a session of the profile with its counts and revision.
func (s *Store) GetSession(ctx context.Context, profileID string, id int64, actor Actor) (Session, error) {
	if err := readerOf(actor, id); err != nil {
		return Session{}, err
	}
	var out Session
	err := s.snapshot(ctx, func(x dbx) error {
		var err error
		out, err = s.sessionTx(ctx, x, profileID, id)
		return err
	})
	if err != nil {
		return Session{}, err
	}
	return out, nil
}

// ListSessions lists the profile's sessions, newest first; archived ones only when asked
// for. An agent bound to a session lists that one only.
func (s *Store) ListSessions(ctx context.Context, profileID string, f SessionFilter, actor Actor) ([]Session, error) {
	if err := readerOf(actor, 0); err != nil {
		return nil, err
	}
	q := `SELECT ` + sessionCols + ` FROM task_sessions WHERE profile_id = ?`
	args := []any{profileID}
	if f.State != "" {
		q += ` AND state = ?`
		args = append(args, string(f.State))
	}
	if !f.IncludeArchived {
		q += ` AND archived_at = ''`
	}
	if actor.Session != 0 {
		q += ` AND id = ?`
		args = append(args, actor.Session)
	}
	q += ` ORDER BY id DESC`
	out := []Session{}
	err := s.snapshot(ctx, func(x dbx) error {
		rows, err := x.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("tasks: reading sessions: %w", err)
		}
		var list []Session
		for rows.Next() {
			se, err := s.scanSession(rows)
			if err != nil {
				rows.Close()
				return fmt.Errorf("tasks: reading sessions: %w", err)
			}
			list = append(list, se)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("tasks: reading sessions: %w", err)
		}
		for _, se := range list {
			if se, err = s.fillSession(ctx, x, se); err != nil {
				return err
			}
			out = append(out, se)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SessionRev is a session's board revision: 0 until its first write.
func (s *Store) SessionRev(ctx context.Context, id int64) (int64, error) {
	var rev int64
	err := s.db.QueryRowContext(ctx, `SELECT rev FROM task_session_rev WHERE session_id = ?`, id).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("tasks: reading the session revision: %w", err)
	}
	return rev, nil
}

// SessionEvents returns the session's history, oldest first, the newest limit of them
// when limit is above 0.
func (s *Store) SessionEvents(ctx context.Context, profileID string, id int64, limit int, actor Actor) ([]SessionEvent, error) {
	if err := readerOf(actor, id); err != nil {
		return nil, err
	}
	out := []SessionEvent{}
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.sessionTx(ctx, x, profileID, id); err != nil {
			return err
		}
		q := `SELECT id, at, actor, kind, note FROM (SELECT id, at, actor, kind, note FROM task_session_events WHERE session_id = ? ORDER BY id DESC`
		args := []any{id}
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
		rows, err := x.QueryContext(ctx, q+`) ORDER BY id`, args...)
		if err != nil {
			return fmt.Errorf("tasks: reading session events: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e SessionEvent
			var at string
			if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Kind, &e.Note); err != nil {
				return fmt.Errorf("tasks: reading session events: %w", err)
			}
			if e.At, err = storedTime("time of a session event", at); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
