package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// evScope is where a task's evidence is looked for, read by the store (the checker never
// takes it from the caller): whether the session belongs to a conversation, that
// conversation's folder, and the HEAD the conductor recorded when the task first started.
type evScope struct {
	scoped    bool
	dir, base string
}

func (sc evScope) check(session, task int64, kind, ref string, files []string) EvidenceCheck {
	return EvidenceCheck{Session: session, Task: task, Kind: kind, Ref: ref, Files: files, Scoped: sc.scoped, Dir: sc.dir, Base: sc.base}
}

// evidenceScope reads the scope of a task. A session with no conversation (the operator's own)
// is unscoped. A conversation whose folder is missing or not absolute leaves dir empty.
func (s *Store) evidenceScope(ctx context.Context, x dbx, profileID string, session, task int64) (evScope, error) {
	var sc evScope
	if session != 0 {
		var conv string
		err := x.QueryRowContext(ctx, `SELECT COALESCE(conversation_id, '') FROM task_sessions WHERE id = ? AND profile_id = ?`, session, profileID).Scan(&conv)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return sc, fmt.Errorf("tasks: reading the session of #%d: %w", task, err)
		}
		if conv != "" {
			sc.scoped = true
			var cwd string
			err := x.QueryRowContext(ctx, `SELECT COALESCE(cwd, '') FROM ai_chat_conversations WHERE id = ? AND profile_id = ?`, conv, profileID).Scan(&cwd)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return sc, fmt.Errorf("tasks: reading the folder of the conversation of #%d: %w", task, err)
			}
			if cwd = strings.TrimSpace(cwd); filepath.IsAbs(cwd) {
				sc.dir = filepath.Clean(cwd)
			}
		}
	}
	// The earliest base stands: only the operator's process writes one, once per task.
	err := x.QueryRowContext(ctx, `SELECT ref FROM task_artifacts WHERE task_id = ? AND kind = ? ORDER BY id ASC LIMIT 1`, task, ArtifactBase).Scan(&sc.base)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return sc, fmt.Errorf("tasks: reading the start of #%d: %w", task, err)
	}
	return sc, nil
}

// evidenceVerdict is the outcome of the evidence gate, computed before the write transaction
// opens. The transaction reads the same state again and uses the verdict only if its key is
// unchanged: else a retryable refusal, never a verdict about older evidence.
type evidenceVerdict struct {
	key string
	err error
}

// evState is what the evidence rule reads of a task.
type evState struct {
	task     Task
	code     bool
	lastFail int64
	commits  []string
	held     bool
	scope    evScope
}

func (st evState) key() string {
	return fmt.Sprintf("%d|%s|%s|%d|%t|%q|%s|%s|%s", st.task.ID, st.task.Kind, st.task.Notes, st.lastFail, st.scope.scoped,
		st.scope.dir, st.scope.base, strings.Join(st.commits, ","), st.task.Status)
}

// loadEvidence reads the task, its evidence artifacts and its scope.
func (s *Store) loadEvidence(ctx context.Context, x dbx, profileID string, session, id int64) (evState, error) {
	t, err := s.getTx(ctx, x, profileID, id)
	if err != nil {
		return evState{}, err
	}
	st := evState{task: t}
	if (t.Kind != "" && t.Kind != KindTask) || !declaresFiles(t.Notes) {
		return st, nil
	}
	st.code = true
	// After a failed verification only evidence attached since counts: the commit that failed
	// the check must not carry the retry through. An artifact is told by its event (the ids of
	// one table are ordered, and the clock may not tick between two writes).
	if err := x.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM task_events WHERE task_id = ? AND kind = 'verify_failed'`, id).Scan(&st.lastFail); err != nil {
		return st, fmt.Errorf("tasks: reading the verifications of #%d: %w", id, err)
	}
	rows, err := x.QueryContext(ctx, `SELECT note FROM task_events WHERE task_id = ? AND kind = 'artifact' AND id > ? ORDER BY id`, id, st.lastFail)
	if err != nil {
		return st, fmt.Errorf("tasks: reading the evidence of #%d: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var note string
		if err := rows.Scan(&note); err != nil {
			return st, fmt.Errorf("tasks: reading the evidence of #%d: %w", id, err)
		}
		kind, ref, _ := strings.Cut(note, " ")
		if evidenceKinds[kind] {
			st.held = true
		}
		if kind == ArtifactCommit {
			st.commits = append(st.commits, ref)
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	rows.Close()
	if s.checker != nil {
		if st.scope, err = s.evidenceScope(ctx, x, profileID, session, id); err != nil {
			return st, err
		}
	}
	return st, nil
}

// precheckEvidence runs the git gate for a task before any write transaction: the SQLite
// write lock is never held while git runs. It is nil when no checker is installed, the task is
// not a code task or its state cannot be read (the transaction then refuses a code task with a
// retryable error). Its refusal is kept in the verdict, not returned: the transaction raises it
// where the check always was, so the order of the other refusals is unchanged.
func (s *Store) precheckEvidence(ctx context.Context, profileID string, session, id int64) *evidenceVerdict {
	if s.checker == nil {
		return nil
	}
	st, err := s.loadEvidence(ctx, s.db, profileID, session, id)
	if err != nil || !st.code {
		return nil
	}
	return &evidenceVerdict{key: st.key(), err: s.gateEvidence(ctx, session, st.task, st.commits, st.scope)}
}

// evidenceMoved is the retryable refusal of a task whose state changed under a check.
func evidenceMoved(id int64) error {
	return sessionNotReady("task #%d changed while its evidence was being checked: try again", id)
}

// attachScope reads the scope for an artifact attach before the transaction.
func (s *Store) attachScope(ctx context.Context, profileID string, session, id int64) (evScope, error) {
	return s.evidenceScope(ctx, s.db, profileID, session, id)
}
