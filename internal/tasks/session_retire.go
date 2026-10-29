package tasks

import (
	"context"
	"database/sql"
	"fmt"
)

// WorkerRetirement is what RetireWorker reports.
type WorkerRetirement struct {
	Session int64 `json:"session"`
	Task    int64 `json:"task"`
	// AlreadyRetired: the worker of this task was retired before and no worker has taken the
	// task since; nothing was written.
	AlreadyRetired bool `json:"already_retired"`
}

// missionTokenTimeFmt is how agent_tokens stores its times (internal/agenttokens).
const missionTokenTimeFmt = "2006-01-02T15:04:05.000Z"

// RetireWorker records that the operator retired the idle worker of a session task: a
// `worker_retired` event of the session, and nothing else here. The task, its comments, artifacts,
// commit references and result are not touched: they are the evidence. The conductor and the chat's
// stored workers read the event to stop the lead resuming the worker (dynorg).
//
// Only the operator may. The worker must be idle: the task is not claimed and no mission token of its
// session and task is live (not revoked, not expired). Both are judged in the transaction that writes
// the event, so a claim or a token created between the check and the write cannot be missed. A task no
// worker ever took is refused. A second retire, with no worker having taken the task since, is a no-op.
func (s *Store) RetireWorker(ctx context.Context, profileID string, sessionID, id int64, note string, actor Actor) (WorkerRetirement, error) {
	if actor.Kind != Human {
		return WorkerRetirement{}, operatorOnly("retire a session's worker")
	}
	out := WorkerRetirement{Session: sessionID, Task: id}
	_, err := s.sessionWrite(ctx, profileID, sessionID, func(x dbx, se Session) error {
		if se.Archived {
			return sessionNotReady("session #%d is archived", sessionID)
		}
		t, err := s.sessionTaskTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if t.SessionID != sessionID {
			return sessionNotFound(sessionID)
		}
		if t.Claim != nil {
			return sessionNotReady("task #%d is held by %s: a worker is still on it", id, t.Claim.By)
		}
		var lastClaim int64
		if err := x.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM task_events WHERE task_id = ? AND kind = 'claimed'`, id).Scan(&lastClaim); err != nil {
			return fmt.Errorf("tasks: reading the claims of task #%d: %w", id, err)
		}
		if lastClaim == 0 {
			return invalid("no worker has taken task #%d", id)
		}
		var live int
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_tokens
			WHERE mission_session = ? AND mission_task = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`,
			sessionID, id, s.now().UTC().Format(missionTokenTimeFmt)).Scan(&live); err != nil {
			return fmt.Errorf("tasks: reading the mission tokens of task #%d: %w", id, err)
		}
		if live > 0 {
			return sessionNotReady("task #%d still has a live mission token: its worker is running", id)
		}
		lastRetire, err := s.lastRetirement(ctx, x, sessionID, id)
		if err != nil {
			return err
		}
		if lastRetire != 0 && !s.claimedAfter(ctx, x, id, lastRetire) {
			out.AlreadyRetired = true
			// Still ends any stored worker of the task: one saved by a turn that was running when
			// the first retirement was written.
			return s.endStoredWorkers(ctx, x, profileID, sessionID, id)
		}
		if err := s.endStoredWorkers(ctx, x, profileID, sessionID, id); err != nil {
			return err
		}
		text := fmt.Sprintf("task #%d", id)
		if note = cleanText(note); note != "" {
			text += ": " + note
		}
		return s.sessionEvent(ctx, x, sessionID, actor, "worker_retired", text)
	})
	if err != nil {
		return WorkerRetirement{}, err
	}
	return out, nil
}

// WorkerRetired says whether the operator retired the worker of the task and no worker has taken the
// task since. A conductor asks before it resumes an idle worker (dynorg).
func (s *Store) WorkerRetired(ctx context.Context, profileID string, sessionID, id int64) (bool, error) {
	retired := false
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.sessionTx(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		last, err := s.lastRetirement(ctx, x, sessionID, id)
		if err != nil {
			return err
		}
		retired = last != 0 && !s.claimedAfter(ctx, x, id, last)
		return nil
	})
	return retired, err
}

// endStoredWorkers drops the session of the dynamic-org workers stored for the task and marks
// them retired; their reports stay. The lead cannot resume a stored worker without a session, and
// the chat does not load a retired one (internal/cli/chat).
func (s *Store) endStoredWorkers(ctx context.Context, x dbx, profileID string, sessionID, id int64) error {
	if _, err := x.ExecContext(ctx, `UPDATE ai_chat_org_workers SET session_id = '', outcome = 'retired'
		WHERE profile_id = ? AND mission_session = ? AND mission_task = ?`, profileID, sessionID, id); err != nil {
		return fmt.Errorf("tasks: ending the stored workers of task #%d: %w", id, err)
	}
	return nil
}

// retirementNote is how a retirement event names the task, the start of its note.
func retirementNote(id int64) string { return fmt.Sprintf("task #%d", id) }

// lastRetirement is the id of the newest `worker_retired` event of the session that names the task, 0 if none.
func (s *Store) lastRetirement(ctx context.Context, x dbx, sessionID, id int64) (int64, error) {
	var ev sql.NullInt64
	n := retirementNote(id)
	err := x.QueryRowContext(ctx, `SELECT MAX(id) FROM task_session_events WHERE session_id = ? AND kind = 'worker_retired' AND (note = ? OR note LIKE ?)`,
		sessionID, n, n+": %").Scan(&ev)
	if err != nil {
		return 0, fmt.Errorf("tasks: reading the retirements of session #%d: %w", sessionID, err)
	}
	return ev.Int64, nil
}

// claimedAfter says whether a worker took the task after the retirement event with this id. The two
// event tables have their own counters, so it compares times: a claim at or after the retirement.
func (s *Store) claimedAfter(ctx context.Context, x dbx, id, retirementID int64) bool {
	var at string
	if x.QueryRowContext(ctx, `SELECT at FROM task_session_events WHERE id = ?`, retirementID).Scan(&at) != nil {
		return true // cannot tell: treat the retirement as stale and record a new one
	}
	var n int
	if x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE task_id = ? AND kind = 'claimed' AND at > ?`, id, at).Scan(&n) != nil {
		return true
	}
	return n > 0
}
