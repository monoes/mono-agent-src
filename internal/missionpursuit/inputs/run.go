package inputs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// Check reads the run state of a session's pursuit in one statement. An error comes with Running false.
func (r *Reader) Check(ctx context.Context, session int64) (Run, error) {
	unreadable := func(err error) (Run, error) {
		return Run{Why: ReasonUnreadable}, fmt.Errorf("missionpursuit inputs: reading the run state: %w", err)
	}
	if session < 1 {
		return unreadable(errors.New("the session id is not a positive integer"))
	}
	if err := ctx.Err(); err != nil {
		return unreadable(err)
	}
	var state, kind string
	var hasRow, enabled int
	var pausedBy string
	err := r.db.QueryRowContext(ctx, `SELECT s.state, s.kind, p.session_id IS NOT NULL, COALESCE(p.enabled, 0), COALESCE(p.paused_by, '')
FROM task_sessions s LEFT JOIN mission_pursuit p ON p.session_id = s.id WHERE s.id = ?`, session).Scan(&state, &kind, &hasRow, &enabled, &pausedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{Why: ReasonNoSession}, nil
	}
	if err != nil {
		return unreadable(err)
	}
	if enabled != 0 && enabled != 1 {
		return unreadable(fmt.Errorf("enabled = %d", enabled))
	}
	switch {
	case kind != "mission":
		return Run{Why: ReasonNotMission}, nil
	case state != "running":
		return Run{Why: ReasonSessionNotRunning}, nil
	case hasRow == 0 || enabled != 1:
		return Run{Why: ReasonNotStarted}, nil
	case pausedBy != "":
		return Run{Why: ReasonPaused}, nil
	}
	return Run{Running: true, Why: ReasonRunning}, nil
}

// sessionOf maps the library's (profile, generation) to the task session: the profile is the session id in plain decimal and
// the generation is 1, as in the mission strategy store.
func sessionOf(profile string, gen int64) (int64, error) {
	id, err := strconv.ParseInt(profile, 10, 64)
	if gen != 1 || err != nil || id < 1 || strconv.FormatInt(id, 10) != profile {
		return 0, errors.New("missionpursuit inputs: a pursuit run is (decimal session id, generation 1)")
	}
	return id, nil
}

// Running is the policy intake's RunState: true only for a running pursuit; any error means not running.
func (r *Reader) Running(ctx context.Context, profile string, gen int64) (bool, error) {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return false, err
	}
	run, err := r.Check(ctx, session)
	if err != nil {
		return false, err
	}
	return run.Running, nil
}

// SessionRun is the epoch engine's RunState for one session.
type SessionRun struct {
	r       *Reader
	session int64
}

// RunOf is the epoch.RunState of the session.
func (r *Reader) RunOf(session int64) SessionRun { return SessionRun{r: r, session: session} }

// Running tells whether the pursuit runs; it reads the rows at every call.
func (s SessionRun) Running(ctx context.Context) (bool, error) {
	if s.r == nil {
		return false, errors.New("missionpursuit inputs: no reader")
	}
	run, err := s.r.Check(ctx, s.session)
	if err != nil {
		return false, err
	}
	return run.Running, nil
}
