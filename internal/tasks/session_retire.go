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
	// StoredWorkersEnded is how many stored dynamic-org workers linked to the task were ended.
	StoredWorkersEnded int `json:"stored_workers_ended"`
	// UnmatchedStoredWorkers is how many stored workers of the session's conversation have no link to
	// any task and still hold a session (saved before migration 147, or not mission workers): their
	// purpose cannot be determined, so the retirement leaves them, and a later turn may load them.
	UnmatchedStoredWorkers int `json:"unmatched_stored_workers"`
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
		claims, err := s.claimCount(ctx, x, id)
		if err != nil {
			return err
		}
		if claims == 0 {
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
		retiredAt, err := s.retiredAtClaims(ctx, x, sessionID, id)
		if err != nil {
			return err
		}
		if retiredAt == claims {
			out.AlreadyRetired = true
			// Still ends any stored worker of the task: one saved by a turn that was running when
			// the first retirement was written.
			return s.endStoredWorkers(ctx, x, profileID, sessionID, id, claims, &out)
		}
		if err := s.endStoredWorkers(ctx, x, profileID, sessionID, id, claims, &out); err != nil {
			return err
		}
		text := fmt.Sprintf("%s (claims: %d)", retirementNote(id), claims)
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

// TaskClaims is how many times a worker has taken the task. A worker records it when it starts: it is the
// worker's claim number.
func (s *Store) TaskClaims(ctx context.Context, profileID string, id int64) (int, error) {
	n := 0
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		var err error
		n, err = s.claimCount(ctx, x, id)
		return err
	})
	return n, err
}

// WorkerRetired says whether the operator retired the worker of the task that started under claim number
// gen (0: the task's latest worker). A retirement made when the task had been taken N times retires the
// workers numbered N or less: a worker that takes the task later has a higher number and is not retired,
// and a later claim does not undo the retirement of an earlier worker. A conductor asks before it resumes
// an idle worker (dynorg).
func (s *Store) WorkerRetired(ctx context.Context, profileID string, sessionID, id int64, gen int) (bool, error) {
	retired := false
	err := s.snapshot(ctx, func(x dbx) error {
		if _, err := s.sessionTx(ctx, x, profileID, sessionID); err != nil {
			return err
		}
		at, err := s.retiredAtClaims(ctx, x, sessionID, id)
		if err != nil {
			return err
		}
		if gen <= 0 {
			if gen, err = s.claimCount(ctx, x, id); err != nil {
				return err
			}
		}
		retired = at > 0 && at >= gen
		return nil
	})
	return retired, err
}

// endStoredWorkers drops the session of the dynamic-org workers stored for the task and marks
// them retired; their reports stay. The lead cannot resume a stored worker without a session, and
// the chat does not load a retired one (internal/cli/chat).
func (s *Store) endStoredWorkers(ctx context.Context, x dbx, profileID string, sessionID, id int64, claims int, out *WorkerRetirement) error {
	// Only the workers this retirement covers (claim number up to claims) and only rows that change:
	// a repeated retire reports none.
	res, err := x.ExecContext(ctx, `UPDATE ai_chat_org_workers SET session_id = '', outcome = 'retired'
		WHERE profile_id = ? AND mission_session = ? AND mission_task = ? AND mission_claims <= ? AND (session_id <> '' OR outcome <> 'retired')`,
		profileID, sessionID, id, claims)
	if err != nil {
		return fmt.Errorf("tasks: ending the stored workers of task #%d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	out.StoredWorkersEnded = int(n)
	// Stored workers with no task link (saved before the link existed, or not mission workers) cannot be
	// matched to this task: count the ones that could still be resumed so the operator is told.
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM ai_chat_org_workers w JOIN task_sessions s ON s.conversation_id = w.conversation_id
		WHERE s.id = ? AND w.profile_id = ? AND w.mission_task = 0 AND w.session_id <> '' AND w.outcome <> 'retired'`,
		sessionID, profileID).Scan(&out.UnmatchedStoredWorkers); err != nil {
		return fmt.Errorf("tasks: counting the stored workers of session #%d that could not be matched: %w", sessionID, err)
	}
	return nil
}

// retirementNote is how a retirement event names the task: the start of its note, followed by the
// number of claims the task had then (" (claims: N)") and the operator's note.
func retirementNote(id int64) string { return fmt.Sprintf("task #%d", id) }

// claimCount is how many times a worker took the task. Events are never deleted, so it only grows: a
// retirement made at N claims stands until a worker takes the task again. It needs no clock, which the
// store stamps to the second.
func (s *Store) claimCount(ctx context.Context, x dbx, id int64) (int, error) {
	var n int
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_events WHERE task_id = ? AND kind = 'claimed'`, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("tasks: counting the claims of task #%d: %w", id, err)
	}
	return n, nil
}

// retiredAtClaims is the claim count the newest retirement of the task was made at, 0 if it was never retired.
func (s *Store) retiredAtClaims(ctx context.Context, x dbx, sessionID, id int64) (int, error) {
	var note sql.NullString
	n := retirementNote(id)
	err := x.QueryRowContext(ctx, `SELECT note FROM task_session_events WHERE session_id = ? AND kind = 'worker_retired' AND note LIKE ? ORDER BY id DESC LIMIT 1`,
		sessionID, n+" (claims: %").Scan(&note)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("tasks: reading the retirements of session #%d: %w", sessionID, err)
	}
	var claims int
	if _, err := fmt.Sscanf(note.String, n+" (claims: %d)", &claims); err != nil {
		return 0, nil // not a retirement written by this code: it stands for nothing
	}
	return claims, nil
}
