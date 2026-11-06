package missionpursuit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// tx runs fn in a BEGIN IMMEDIATE transaction on a connection of its own, so that the checks fn reads (the chat mode, the
// session, the goal) hold until the commit. An error from fn rolls back.
func (s *Store) tx(ctx context.Context, fn func(x tasks.Queryer) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("missionpursuit: connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return fmt.Errorf("missionpursuit: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
		return fmt.Errorf("missionpursuit: commit: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) read(ctx context.Context, fn func(x tasks.Queryer) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("missionpursuit: connection: %w", err)
	}
	defer conn.Close()
	return fn(conn)
}

// checkMission refuses unless the session is a live mission session of the profile and its chat is in mode mission.
// Everything unreadable or unknown is a refusal: it fails closed.
func (s *Store) checkMission(ctx context.Context, x tasks.Queryer, profileID string, session int64) (string, error) {
	var kind, createdBy, state string
	var conv sql.NullString
	err := x.QueryRowContext(ctx, `SELECT kind, created_by, state, conversation_id FROM task_sessions WHERE id = ? AND profile_id = ?`, session, profileID).Scan(&kind, &createdBy, &state, &conv)
	if err != nil {
		return "", notFound(err, session)
	}
	isMission := kind == tasks.SessionKindMission || (kind == tasks.SessionKindBoard && createdBy == tasks.MissionPlannerName)
	if !isMission {
		return "", invalid("session #%d is not a mission session: only a mission has an ultimate goal", session)
	}
	switch state {
	case string(tasks.SessionDraft), string(tasks.SessionRunning), string(tasks.SessionPaused):
	default:
		return "", invalid("session #%d is %s: the goal of an ended mission is not written", session, state)
	}
	if !conv.Valid || conv.String == "" {
		return "", invalid("session #%d has no chat, so it has no chat mode: the goal is written for a mission chat", session)
	}
	mode, err := s.modes.ChatMode(ctx, x, profileID, conv.String)
	if err != nil {
		return "", err
	}
	if mode != ChatModeMission {
		return "", invalid("the chat of session #%d is not in mission mode (it is %q): only a mission chat has an ultimate goal", session, mode)
	}
	return conv.String, nil
}

// errDamaged: the criteria rows of a goal do not hash to the goal's hash. readGoal still returns the goal's state.
var errDamaged = errors.New("missionpursuit: the goal record is damaged")

// readGoal reads the goal of a session and checks that its criteria rows still hash to the goal's hash.
func readGoal(ctx context.Context, x tasks.Queryer, session int64) (Goal, error) {
	g := Goal{SessionID: session}
	err := x.QueryRowContext(ctx, `SELECT state, criteria_hash, proposed_by, confirmed_by, confirmed_at, locked_at FROM mission_goals WHERE session_id = ?`, session).
		Scan(&g.State, &g.Hash, &g.ProposedBy, &g.ConfirmedBy, &g.ConfirmedAt, &g.LockedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Goal{}, fmt.Errorf("%w: session #%d has no mission goal yet", tasks.ErrNotFound, session)
	}
	if err != nil {
		return Goal{}, fmt.Errorf("missionpursuit: reading the goal: %w", err)
	}
	rows, err := x.QueryContext(ctx, `SELECT pos, kind, spec, expected, hash FROM mission_goal_criteria WHERE session_id = ? ORDER BY pos`, session)
	if err != nil {
		return Goal{}, fmt.Errorf("missionpursuit: reading the criteria: %w", err)
	}
	defer rows.Close()
	var plain []Criterion
	for rows.Next() {
		var pos int
		var sc StoredCriterion
		if err := rows.Scan(&pos, &sc.Kind, &sc.Spec, &sc.Expected, &sc.Hash); err != nil {
			return Goal{}, fmt.Errorf("missionpursuit: reading a criterion: %w", err)
		}
		sc.ID = fmt.Sprintf("c%d", pos)
		g.Criteria = append(g.Criteria, sc)
		plain = append(plain, sc.Criterion)
	}
	if err := rows.Err(); err != nil {
		return Goal{}, err
	}
	if Hash(plain) != g.Hash {
		fix := "a confirmed goal never changes, so it cannot be repaired"
		if g.State == StateProposed {
			fix = "propose the criteria again to replace it"
		}
		return g, fmt.Errorf("%w: the criteria of session #%d do not match their hash; %s", errDamaged, session, fix)
	}
	return g, nil
}
