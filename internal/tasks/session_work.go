package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// depSettledSQL is, for a dependency row aliased p, the one condition under which a session
// task's dependency no longer holds its dependents back: done, or verified and waiting in
// Review for the operator to close (not blocked). With auto_close_verified off (the
// default) a verified task stays in Review, so waiting for Done would stall the mission.
// SessionNext, SessionReady, SessionClaim and DepSettled all use it; they cannot differ.
const depSettledSQL = `(p.status = 'done' OR (p.status = 'review' AND p.verified = 1 AND p.blocked = 0))`

// DepSettled says whether a task counts as finished for the tasks that depend on it.
func DepSettled(t Task) bool {
	return t.Status == StatusDone || (t.Status == StatusReview && t.Verified && !t.Blocked)
}

// readyInSession is the condition of a task that SessionNext may hand out: Ready, not an
// epic, not blocked, with every dependency done and room left in its history.
const readyInSession = `session_id = ? AND kind <> 'epic' AND blocked = 0 AND status = 'ready'
	AND NOT EXISTS (SELECT 1 FROM task_deps d JOIN tasks p ON p.id = d.depends_on_task_id WHERE d.task_id = tasks.id AND NOT ` + depSettledSQL + `)
	AND (SELECT COUNT(*) FROM task_events WHERE task_id = tasks.id) < ?`

// pickSessionNext is the id of the task SessionNext would take at now: the top of the
// session's Ready tasks whose dependencies are done, else the stale claim whose lease ended
// first. 0 means there is none.
func (s *Store) pickSessionNext(ctx context.Context, x dbx, session int64, now time.Time) (int64, error) {
	var id int64
	err := x.QueryRowContext(ctx, `SELECT id FROM tasks WHERE `+readyInSession+` ORDER BY position, id LIMIT 1`, session, MaxEventsToClaim).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("tasks: picking the next task: %w", err)
	}
	err = x.QueryRowContext(ctx,
		`SELECT id FROM tasks WHERE session_id = ? AND status = 'in_progress' AND claimed_by <> '' AND claim_until <= ?
		   AND (SELECT COUNT(*) FROM task_events WHERE task_id = tasks.id) < ?
		 ORDER BY claim_until, id LIMIT 1`, session, now.Format(timeFmt), MaxEventsToClaim).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("tasks: picking the next task: %w", err)
	}
	return id, nil
}

// runnable refuses work on a session that does not run, or has spent its budget.
func runnable(se Session) error {
	if se.State != SessionRunning || se.Archived {
		return sessionNotReady("session #%d is %s: only a running session hands out tasks", se.ID, closedWord(se))
	}
	return overBudget(se)
}

// SessionNext returns the task an agent should work next in a session: a Ready task whose
// dependencies are all done, of a RUNNING session. A session that is a draft, paused or
// closed hands out nothing (nil, no error). Without claim it only looks; with claim it
// picks and claims in one write transaction and needs a named agent. A session that has
// spent its budget is refused (ErrLimit).
func (s *Store) SessionNext(ctx context.Context, profileID string, sessionID int64, actor Actor, claim bool, lease time.Duration) (*Task, error) {
	if err := bound(actor, sessionID); err != nil {
		return nil, err
	}
	if claim {
		if err := needName(actor); err != nil {
			return nil, err
		}
		if err := noPicking(actor, "take the next task"); err != nil {
			return nil, err
		}
		if err := noVerifier(actor, "take the next task"); err != nil {
			return nil, err
		}
	} else if actor.Kind != Human && actor.Kind != Agent {
		return nil, operatorOnly("read a session's tasks")
	}
	var out *Task
	run := s.snapshot
	if claim {
		run = s.tx
	}
	err := run(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		se, err := s.sessionTx(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		if se.State != SessionRunning || se.Archived {
			return nil
		}
		if err := overBudget(se); err != nil {
			return err
		}
		now := s.now().UTC()
		id, err := s.pickSessionNext(ctx, x, sessionID, now)
		if err != nil || id == 0 {
			return err
		}
		if claim {
			if _, err := s.claimTx(ctx, x, profileID, id, actor, now, lease); err != nil {
				return err
			}
			if err := s.bumpSession(ctx, x, sessionID); err != nil {
				return err
			}
		}
		t, err := s.sessionTaskTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		out = &t
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SessionClaim takes a Ready task of a running session for a named agent, or renews the
// agent's hold on one; a task whose dependencies are not all done, an epic and a blocked
// task are refused.
func (s *Store) SessionClaim(ctx context.Context, profileID string, sessionID, id int64, actor Actor, lease time.Duration) (Task, error) {
	var out Task
	_, err := s.claimIn(ctx, profileID, sessionID, id, actor, lease, func(x dbx) error {
		se, err := s.sessionTx(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		if err := runnable(se); err != nil {
			return err
		}
		var n int
		err = x.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = ? AND status = 'ready' AND (kind = 'epic' OR blocked = 1)`, id).Scan(&n)
		if err != nil {
			return fmt.Errorf("tasks: reading #%d: %w", id, err)
		}
		if n > 0 {
			return sessionNotReady("task #%d is an epic or is blocked: it is not taken", id)
		}
		var waiting int
		err = x.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM task_deps d JOIN tasks p ON p.id = d.depends_on_task_id
			 WHERE d.task_id = ? AND NOT `+depSettledSQL+` AND (SELECT status FROM tasks WHERE id = ?) = 'ready'`, id, id).Scan(&waiting)
		if err != nil {
			return fmt.Errorf("tasks: reading the dependencies of #%d: %w", id, err)
		}
		if waiting > 0 {
			return sessionNotReady("task #%d waits for %d tasks that are neither done nor verified", id, waiting)
		}
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	err = s.snapshot(ctx, func(x dbx) error {
		var err error
		out, err = s.sessionTaskTx(ctx, x, profileID, id)
		return err
	})
	return out, err
}

// SessionComment is Comment on a task of the session.
func (s *Store) SessionComment(ctx context.Context, profileID string, sessionID, id int64, text string, actor Actor) (Task, error) {
	return s.commentIn(ctx, profileID, sessionID, id, text, actor)
}

// SessionFinish is Finish on a task of the session: the task goes to Review.
//
// A code task (kind task, files declared in its notes) is handed to review with a result
// only when it holds evidence: a commit, diff, test, file or log artifact (ErrNoEvidence).
// A report does not count, and a question needs none.
func (s *Store) SessionFinish(ctx context.Context, profileID string, sessionID, id int64, o Outcome, actor Actor) (Task, error) {
	if err := s.agentWritable(ctx, profileID, sessionID, actor, "finish a task"); err != nil {
		return Task{}, err
	}
	return s.finishIn(ctx, profileID, sessionID, id, o, actor)
}

// SessionRelease is Release on a task of the session.
func (s *Store) SessionRelease(ctx context.Context, profileID string, sessionID, id int64, note string, actor Actor) (Task, error) {
	return s.releaseIn(ctx, profileID, sessionID, id, note, actor)
}

// lastHandBack is who handed the task back last and how: "result" or "question".
func lastHandBack(ctx context.Context, x dbx, id int64) (actor, kind string, err error) {
	err = x.QueryRowContext(ctx,
		`SELECT actor, kind FROM task_events WHERE task_id = ? AND kind IN ('result', 'question') ORDER BY id DESC LIMIT 1`, id).Scan(&actor, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return actor, kind, err
}

// identityRE is the "#<4 hex>" the MCP server appends to an agent's name, one per connection.
var identityRE = regexp.MustCompile(`#[0-9a-fA-F]{4}$`)

// identityOf is the agent behind a name: the name without the connection suffix, in lower case,
// so that agent:claude#ab12 and agent:claude#cd34 (two connections of one client) are one agent.
func identityOf(name string) string {
	return strings.ToLower(identityRE.ReplaceAllString(name, ""))
}

// sameIdentity says whether two labels are one agent. It is a name check on top of the
// binding: an agent that picks a name that is not its client's gets past it, which is why a
// worker bound to a task cannot verify at all.
func sameIdentity(a, b string) bool { return identityOf(a) == identityOf(b) }

// VerifySessionTask records a verifier's verdict on a task in Review. A pass marks the task
// verified and nothing more: closing it to Done is the operator's, or the verifier's only
// when the session's auto_close_verified is on (D1). A fail returns the task to Ready with
// the note, up to the session's max_retries failures; one more and the task is blocked (it
// stays in Review, flagged, for the operator). The verifier is the operator or a named agent
// of the session: never one bound to the task as its worker (only a verifier binding passes),
// never the same agent identity as the last hand-back (sameIdentity), and an agent only while
// the session runs (the operator may act while it is paused). A task that holds a question is
// not verified: the operator answers it.
func (s *Store) VerifySessionTask(ctx context.Context, profileID string, sessionID, id int64, pass bool, note string, actor Actor) (Task, error) {
	if err := actorIn(actor, sessionID); err != nil {
		return Task{}, err
	}
	if actor.Task != 0 && !actor.Verifier {
		return Task{}, bindingRefused("this agent is bound to task #%d as its worker: it cannot record a verdict on it", actor.Task)
	}
	note = cutBytes(cleanText(note), MaxCommentBytes)
	if !pass && note == "" {
		return Task{}, invalid("a failed verification needs a note: say what is wrong, the next worker reads it")
	}
	var out Task
	err := s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		se, err := s.sessionTx(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		if (se.State != SessionRunning && se.State != SessionPaused) || se.Archived {
			return sessionNotReady("session #%d is %s: tasks are verified in a session that runs", sessionID, closedWord(se))
		}
		if err := writableBy(se, actor, "verify a task"); err != nil {
			return err
		}
		if err := s.checkScope(ctx, x, profileID, id, sessionID, actor); err != nil {
			return err
		}
		cur, err := s.getTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if cur.Status != StatusReview || cur.Blocked {
			return invalid("task #%d is %s: only a task in Review that is not blocked is verified", id, cur.Status)
		}
		if cur.Verified {
			return invalid("task #%d is verified already", id)
		}
		worker, kind, err := lastHandBack(ctx, x, id)
		if err != nil {
			return fmt.Errorf("tasks: reading the last hand-back of #%d: %w", id, err)
		}
		if kind == "question" {
			return invalid("task #%d holds a question: the operator answers it before it is verified", id)
		}
		if actor.Kind == Agent && worker != "" && sameIdentity(worker, actor.Label()) {
			return invalid("task #%d was done by %s: another agent verifies it", id, worker)
		}
		stamp := s.stamp()
		switch {
		case pass:
			err = s.verifyPass(ctx, x, profileID, se, cur, note, actor, stamp)
		case cur.Retries+1 > se.MaxRetries:
			err = s.verifyBlock(ctx, x, se, cur, note, actor, stamp)
		default:
			err = s.verifyRetry(ctx, x, profileID, cur, note, actor)
		}
		if err != nil {
			return err
		}
		if err := s.touchSession(ctx, x, sessionID); err != nil {
			return err
		}
		out, err = s.sessionTaskTx(ctx, x, profileID, id)
		return err
	})
	if err != nil {
		return Task{}, err
	}
	return out, nil
}

func (s *Store) verifyPass(ctx context.Context, x dbx, profileID string, se Session, cur Task, note string, actor Actor, stamp string) error {
	if _, err := x.ExecContext(ctx, `UPDATE tasks SET verified = 1, updated_at = ? WHERE id = ?`, stamp, cur.ID); err != nil {
		return fmt.Errorf("tasks: verifying #%d: %w", cur.ID, err)
	}
	if err := s.event(ctx, x, cur.ID, stamp, actor.Label(), "verified", string(StatusReview), string(StatusReview), note); err != nil {
		return err
	}
	if !se.AutoCloseVerified {
		return nil
	}
	cur.Verified = true
	return s.moveTx(ctx, x, profileID, cur, StatusDone, Placement{}, actor, "closed", "closed on verification (the operator set auto_close_verified)")
}

// verifyBlock flags a task that has failed too often: it stays in Review for the operator.
func (s *Store) verifyBlock(ctx context.Context, x dbx, se Session, cur Task, note string, actor Actor, stamp string) error {
	if _, err := x.ExecContext(ctx, `UPDATE tasks SET blocked = 1, retries = retries + 1, updated_at = ? WHERE id = ?`, stamp, cur.ID); err != nil {
		return fmt.Errorf("tasks: blocking #%d: %w", cur.ID, err)
	}
	if err := s.event(ctx, x, cur.ID, stamp, actor.Label(), "blocked", string(StatusReview), string(StatusReview),
		fmt.Sprintf("failed verification %d times (max retries %d): %s", cur.Retries+1, se.MaxRetries, note)); err != nil {
		return err
	}
	return s.sessionEvent(ctx, x, se.ID, actor, "blocked", fmt.Sprintf("#%d %s", cur.ID, cur.Title))
}

// verifyRetry sends a task that failed back to Ready, behind the others, with the note.
func (s *Store) verifyRetry(ctx context.Context, x dbx, profileID string, cur Task, note string, actor Actor) error {
	if err := s.moveTx(ctx, x, profileID, cur, StatusReady, Placement{Bottom: true}, actor, "verify_failed", note); err != nil {
		return err
	}
	if _, err := x.ExecContext(ctx, `UPDATE tasks SET retries = retries + 1 WHERE id = ?`, cur.ID); err != nil {
		return fmt.Errorf("tasks: counting a retry of #%d: %w", cur.ID, err)
	}
	return nil
}

// Artifact kinds only the operator's process writes (the conductor, for an isolated writer):
// branch (ref = the writer's branch, note = who and where) and merge (ref = that branch,
// once org_merge brought it in). A mission's pending merges are computed from them.
const (
	ArtifactBranch = "branch"
	ArtifactMerge  = "merge"
	// FinalReportRef is the reference of a mission's final report (kind report): only the
	// operator's process writes it, so that its presence is the operator's own record.
	FinalReportRef = "final-report"
)

var artifactKinds = map[string]bool{"commit": true, "file": true, "report": true, "test": true, "diff": true, "log": true, "url": true, "other": true, ArtifactBranch: true, ArtifactMerge: true}

// artifactRefRunes bounds an artifact's reference.
const artifactRefRunes = 2048

var artifactKindRE = regexp.MustCompile(`^[a-z]{1,16}$`)

// AddArtifact attaches evidence (a commit, a file, a report, test output) to a task of the
// session. The operator may on any of its tasks; an agent only on a task it holds or one
// in Review (a verifier's evidence). A task holds at most MaxArtifactsPerTask.
func (s *Store) AddArtifact(ctx context.Context, profileID string, sessionID, id int64, in ArtifactInput, actor Actor) (Artifact, error) {
	if err := actorIn(actor, sessionID); err != nil {
		return Artifact{}, err
	}
	if !artifactKindRE.MatchString(in.Kind) || !artifactKinds[in.Kind] {
		return Artifact{}, invalid("unknown artifact kind %q (use commit, file, report, test, diff, log, url or other)", echo(in.Kind))
	}
	if (in.Kind == ArtifactBranch || in.Kind == ArtifactMerge) && actor.Kind != Human {
		return Artifact{}, operatorOnly("record a " + in.Kind + " artifact: the scheduler's records of writer branches are the operator's process's")
	}
	ref := cutRunes(oneLine(in.Ref), artifactRefRunes)
	if ref == "" {
		return Artifact{}, invalid("an artifact needs a reference: a commit hash, a path, a URL")
	}
	if in.Kind == "report" && ref == FinalReportRef && actor.Kind != Human {
		return Artifact{}, operatorOnly("attach the reserved " + FinalReportRef + " report: the mission's final report is the operator's process's")
	}
	note := cutBytes(cleanText(in.Note), MaxCommentBytes)
	var out Artifact
	err := s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		se, err := s.sessionTx(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		if err := writableBy(se, actor, "attach an artifact"); err != nil {
			return err
		}
		if err := s.checkScope(ctx, x, profileID, id, sessionID, actor); err != nil {
			return err
		}
		cur, err := s.getTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		if actor.Kind == Agent && cur.Status != StatusReview && (cur.Status != StatusInProgress || cur.Claim == nil || cur.Claim.By != actor.Label()) {
			return notClaimant("task #%d is not held by %s and not in Review: only the holder or a verifier attaches evidence", id, actor.Label())
		}
		var n int
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_artifacts WHERE task_id = ?`, id).Scan(&n); err != nil {
			return fmt.Errorf("tasks: counting artifacts: %w", err)
		}
		if n >= MaxArtifactsPerTask {
			return limit("task #%d already has %d artifacts", id, n)
		}
		stamp := s.stamp()
		res, err := x.ExecContext(ctx, `INSERT INTO task_artifacts (task_id, kind, ref, note, at, actor) VALUES (?,?,?,?,?,?)`,
			id, in.Kind, ref, note, stamp, actor.Label())
		if err != nil {
			return fmt.Errorf("tasks: adding an artifact: %w", err)
		}
		aid, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("tasks: adding an artifact: %w", err)
		}
		if err := s.event(ctx, x, id, stamp, actor.Label(), "artifact", "", "", in.Kind+" "+ref); err != nil {
			return err
		}
		if err := s.touchSession(ctx, x, sessionID); err != nil {
			return err
		}
		at, _ := time.Parse(timeFmt, stamp)
		out = Artifact{ID: aid, TaskID: id, Kind: in.Kind, Ref: ref, Note: note, At: at, Actor: actor.Label()}
		return nil
	})
	if err != nil {
		return Artifact{}, err
	}
	return out, nil
}
