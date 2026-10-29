package tasks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
)

// ErrPlanChanged: the plan the person read is not the plan the draft holds now. Nothing was approved.
var ErrPlanChanged = errors.New("plan changed")

// PlanGuard is what the person's approval was given for: the session revision and, when the caller
// has it, the content digest, both as `person inbox` showed them. A zero guard guards nothing; the
// person verb never passes one without a revision.
type PlanGuard struct {
	Rev    int64
	Digest string // optional; checked when not empty
}

// PlanView is what the person reads about a draft's plan: its revision, how many tasks it
// holds and a digest of everything a lead can change in it.
type PlanView struct {
	Rev       int64
	Digest    string
	TaskCount int
}

func planChanged(id int64, why string) error {
	return &sessionError{ErrPlanChanged, fmt.Sprintf("the plan of session #%d changed since you read it (%s): nothing was approved; read the inbox again (`task session person inbox`) and approve what it shows now", id, why)}
}

// PlanOf reads the revision and digest of a session's plan. It is the operator's.
func (s *Store) PlanOf(ctx context.Context, profileID string, id int64, actor Actor) (PlanView, error) {
	if actor.Kind != Human {
		return PlanView{}, operatorOnly("read a session's plan digest")
	}
	var out PlanView
	err := s.snapshot(ctx, func(x dbx) error {
		se, err := s.sessionTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		out, err = s.planViewTx(ctx, x, se)
		return err
	})
	return out, err
}

// SessionWithPlan reads a session and the revision and digest of its plan in one snapshot, so the
// two always describe the same plan (two reads could straddle a lead's edit). It is the operator's.
func (s *Store) SessionWithPlan(ctx context.Context, profileID string, id int64, actor Actor) (Session, PlanView, error) {
	if actor.Kind != Human {
		return Session{}, PlanView{}, operatorOnly("read a session's plan digest")
	}
	var se Session
	var pv PlanView
	err := s.snapshot(ctx, func(x dbx) error {
		var err error
		if se, err = s.sessionTx(ctx, x, profileID, id); err != nil {
			return err
		}
		pv, err = s.planViewTx(ctx, x, se)
		return err
	})
	return se, pv, err
}

// planViewTx hashes the goal and caps of the session and, for each task it stores in any column,
// the words and structure a lead can write: title, notes, acceptance, kind, parent, assignee,
// suggested executor, the dependencies and how many events (comments) the task holds.
func (s *Store) planViewTx(ctx context.Context, x dbx, se Session) (PlanView, error) {
	ts, err := s.queryTasks(ctx, x, `SELECT `+taskCols+` FROM tasks WHERE session_id = ? ORDER BY id`, se.ID)
	if err != nil {
		return PlanView{}, err
	}
	if err := s.attachDeps(ctx, x, ts); err != nil {
		return PlanView{}, err
	}
	events := map[int64]int{}
	rows, err := x.QueryContext(ctx, `SELECT e.task_id, COUNT(*) FROM task_events e JOIN tasks t ON t.id = e.task_id WHERE t.session_id = ? GROUP BY e.task_id`, se.ID)
	if err != nil {
		return PlanView{}, fmt.Errorf("tasks: reading the plan's events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tid int64
		var n int
		if err := rows.Scan(&tid, &n); err != nil {
			return PlanView{}, fmt.Errorf("tasks: reading the plan's events: %w", err)
		}
		events[tid] = n
	}
	if err := rows.Err(); err != nil {
		return PlanView{}, err
	}
	h := sha256.New()
	field := func(v any) { fmt.Fprintf(h, "%d:%v|", len(fmt.Sprint(v)), v) } // length-prefixed: no field runs into the next
	field(se.Title)
	field(se.Goal)
	field(se.AccessLevel)
	field(se.BudgetUSD)
	field(se.MaxTasks)
	for _, t := range ts {
		deps := slices.Clone(t.DependsOn)
		slices.Sort(deps)
		field(t.ID)
		field(t.Title)
		field(t.Notes)
		field(t.Acceptance)
		field(t.Kind)
		field(t.ParentID)
		field(t.Assignee)
		field(t.SuggestedExecutor)
		field(deps)
		field(events[t.ID])
	}
	return PlanView{Rev: se.Rev, Digest: hex.EncodeToString(h.Sum(nil)), TaskCount: len(ts)}, nil
}

// checkPlanGuard runs inside the approval's write transaction, so no write can slip between the
// check and the approval.
func (s *Store) checkPlanGuard(ctx context.Context, x dbx, se Session, g PlanGuard) error {
	if g.Rev != se.Rev {
		return planChanged(se.ID, fmt.Sprintf("revision %d, now %d", g.Rev, se.Rev))
	}
	if g.Digest == "" {
		return nil
	}
	v, err := s.planViewTx(ctx, x, se)
	if err != nil {
		return err
	}
	if v.Digest != g.Digest {
		return planChanged(se.ID, "its content differs")
	}
	return nil
}

// ApproveSessionAt is the operator's ApproveSession for a plan as it was read: it approves only if
// the plan is still the one the guard names. The approval is the operator's.
func (s *Store) ApproveSessionAt(ctx context.Context, profileID string, id int64, actor Actor, g PlanGuard) (Session, error) {
	if actor.Kind != Human {
		return Session{}, operatorOnly("approve a session's plan as read")
	}
	return s.approveSession(ctx, profileID, id, actor, approveOpts{guard: &g})
}

// ApproveSessionAsPerson is ApproveSessionAt for the person's answer to a giant plan's item (the
// `person approve` verb, and only it): the approval is recorded with the person's basis when the
// session has such an item.
func (s *Store) ApproveSessionAsPerson(ctx context.Context, profileID string, id int64, actor Actor, g PlanGuard) (Session, error) {
	if actor.Kind != Human {
		return Session{}, operatorOnly("approve a session's plan as the person")
	}
	return s.approveSession(ctx, profileID, id, actor, approveOpts{guard: &g, person: true})
}
