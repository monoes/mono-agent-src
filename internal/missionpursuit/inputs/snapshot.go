package inputs

import (
	"context"
	"fmt"
	"strconv"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/strategy"
)

// Snapshot reads the facts of a running pursuit. A pursuit that is not running yields a Snapshot with only Run set; an
// unreadable one yields an error and Running false.
func (r *Reader) Snapshot(ctx context.Context, session int64) (Snapshot, error) {
	run, err := r.Check(ctx, session)
	if err != nil || !run.Running {
		return Snapshot{Run: run}, err
	}
	fail := func(what string, err error) (Snapshot, error) {
		return Snapshot{Run: Run{Why: ReasonUnreadable}}, fmt.Errorf("missionpursuit inputs: reading %s: %w", what, err)
	}
	out := Snapshot{Run: run}
	if out.Tasks, err = r.taskCounts(ctx, session); err != nil {
		return fail("the task counts", err)
	}
	if out.Budget, err = r.budget(ctx, session); err != nil {
		return fail("the budget", err)
	}
	if err = r.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(a.id), 0) FROM task_artifacts a JOIN tasks t ON t.id = a.task_id WHERE t.session_id = ?`, session).Scan(&out.EvidenceSeq); err != nil {
		return fail("the evidence", err)
	}
	st, profile := strategy.NewStore(r.db), strconv.FormatInt(session, 10)
	if out.Directions, err = st.OpenDirections(ctx, profile, 1); err != nil {
		return fail("the directions", err)
	}
	epochs, err := st.ListEpochs(ctx, profile, 1)
	if err != nil {
		return fail("the epochs", err)
	}
	if n := len(epochs); n > 0 {
		out.Epochs.HasLatest, out.Epochs.Latest, out.Epochs.Count = true, epochs[n-1], epochs[n-1].Seq
	}
	if out.Epochs.ParkedFinishRounds, err = st.ParkedFinishRounds(ctx, profile, 1); err != nil {
		return fail("the epochs", err)
	}
	return out, nil
}

func (r *Reader) taskCounts(ctx context.Context, session int64) (TaskCounts, error) {
	var c TaskCounts
	rows, err := r.db.QueryContext(ctx, `SELECT status, blocked, verified, kind = 'epic', COUNT(*) FROM tasks WHERE session_id = ? GROUP BY status, blocked, verified, kind = 'epic'`, session)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var blocked, verified, epic, n int
		if err := rows.Scan(&status, &blocked, &verified, &epic, &n); err != nil {
			return c, err
		}
		c.Total += n
		switch status {
		case "inbox":
			c.Inbox += n
		case "ready":
			c.Ready += n
		case "in_progress":
			c.InProgress += n
		case "review":
			c.Review += n
		case "done":
			c.Done += n
		case "archived":
			c.Archived += n
		default:
			c.Unknown += n
		}
		if blocked != 0 && status != "done" && status != "archived" {
			c.Blocked += n
		}
		if status != "archived" && epic == 0 {
			c.NonEpic += n
		}
		open := status != "done" && status != "archived"
		if open && blocked == 0 && epic == 0 && !(status == "review" && verified != 0) {
			c.Workable += n
		}
	}
	return c, rows.Err()
}

func (r *Reader) budget(ctx context.Context, session int64) (BudgetShare, error) {
	var b, s float64
	if err := r.db.QueryRowContext(ctx, `SELECT budget_usd, spent_usd FROM task_sessions WHERE id = ?`, session).Scan(&b, &s); err != nil {
		return BudgetShare{}, err
	}
	if !missionpursuit.BudgetKnown(b, s) {
		return BudgetShare{BudgetUSD: b, SpentUSD: s}, nil
	}
	return BudgetShare{Known: true, Share: s / b, BudgetUSD: b, SpentUSD: s}, nil
}
