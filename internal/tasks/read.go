package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// statusOrder sorts a query by column, in board order.
const statusOrder = `CASE status WHEN 'inbox' THEN 0 WHEN 'ready' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'review' THEN 3 WHEN 'done' THEN 4 ELSE 5 END`

// Rev is the profile's board revision: 0 until its first write. The profile is
// not checked (an unknown one reads 0): resolve it with Profile first. It is one
// primary-key read, so it needs no snapshot.
func (s *Store) Rev(ctx context.Context, profileID string) (int64, error) {
	return s.revOf(ctx, s.db, profileID)
}

func (s *Store) revOf(ctx context.Context, x dbx, profileID string) (int64, error) {
	var rev int64
	err := x.QueryRowContext(ctx, `SELECT rev FROM task_board_rev WHERE profile_id = ?`, profileID).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("tasks: reading the revision: %w", err)
	}
	return rev, nil
}

// Counts returns the profile's cards per column and its stale claims, read in
// one snapshot. The profile is not checked (an unknown one has no cards):
// resolve it with Profile first.
func (s *Store) Counts(ctx context.Context, profileID string) (Counts, error) {
	var c Counts
	err := s.snapshot(ctx, func(x dbx) error {
		var err error
		c, err = s.countsOf(ctx, x, profileID)
		return err
	})
	if err != nil {
		return Counts{}, err
	}
	return c, nil
}

// countsSQL counts the profile's cards per column of the board. It names the five columns where it could
// ask for every status: the archive has no limit, and a count of the profile's whole range reads all of it,
// at every Counts, every Board and every poll of a Watch, while five equalities are five seeks in
// idx_tasks_board, bounded by the 2,000 open tasks (as openTasksSQL).
const countsSQL = `SELECT status, COUNT(*) FROM tasks WHERE profile_id = ? AND session_id IS NULL AND status IN ('inbox', 'ready', 'in_progress', 'review', 'done') GROUP BY status`

func (s *Store) countsOf(ctx context.Context, x dbx, profileID string) (Counts, error) {
	var c Counts
	rows, err := x.QueryContext(ctx, countsSQL, profileID)
	if err != nil {
		return Counts{}, fmt.Errorf("tasks: counting: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return Counts{}, fmt.Errorf("tasks: counting: %w", err)
		}
		switch Status(st) {
		case StatusInbox:
			c.Inbox = n
		case StatusReady:
			c.Ready = n
		case StatusInProgress:
			c.InProgress = n
		case StatusReview:
			c.Review = n
		case StatusDone:
			c.Done = n
		}
	}
	if err := rows.Err(); err != nil {
		return Counts{}, fmt.Errorf("tasks: counting: %w", err)
	}
	// The loop ran to its end, which closes rows: the next statement is not run
	// beside an open one.
	err = x.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE profile_id = ? AND session_id IS NULL AND status = 'in_progress' AND claimed_by <> '' AND claim_until <= ?`,
		profileID, s.stamp()).Scan(&c.Stale)
	if err != nil {
		return Counts{}, fmt.Errorf("tasks: counting stale claims: %w", err)
	}
	return c, nil
}

// queryTasks runs a query that selects taskCols and returns its tasks with
// their last events. The result is never nil.
func (s *Store) queryTasks(ctx context.Context, x dbx, q string, args ...any) ([]Task, error) {
	rows, err := x.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("tasks: reading tasks: %w", err)
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		t, err := s.scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("tasks: reading tasks: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: reading tasks: %w", err)
	}
	// As in countsOf, rows is closed by now.
	if err := s.attachLast(ctx, x, out); err != nil {
		return nil, err
	}
	if err := s.attachAuthors(ctx, x, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns a task of the profile with its events, oldest first. It takes no
// actor: a task is read by its id, in any column. The profile is not checked: a
// task that is not in the profile and an unknown profile are both ErrNotFound, so
// resolve the profile with Profile first.
func (s *Store) Get(ctx context.Context, profileID string, id int64) (Task, []Event, error) {
	var t Task
	events := []Event{}
	err := s.snapshot(ctx, func(x dbx) error {
		var err error
		if t, err = s.getTx(ctx, x, profileID, id); err != nil {
			return err
		}
		rows, err := x.QueryContext(ctx,
			`SELECT id, at, actor, kind, from_status, to_status, note FROM task_events WHERE task_id = ? ORDER BY id`, id)
		if err != nil {
			return fmt.Errorf("tasks: reading events: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e Event
			var at string
			if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Kind, &e.FromStatus, &e.ToStatus, &e.Note); err != nil {
				return fmt.Errorf("tasks: reading events: %w", err)
			}
			if e.At, err = storedTime("time of an event", at); err != nil {
				return fmt.Errorf("tasks: reading events: %w", err)
			}
			events = append(events, e)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("tasks: reading events: %w", err)
		}
		return nil
	})
	if err != nil {
		return Task{}, nil, err
	}
	return t, events, nil
}

// defaultStatuses is what List shows when no status is named: every column to
// the operator, and only the work that is open to an agent.
func defaultStatuses(actor Actor) []Status {
	if actor.Kind == Agent {
		return []Status{StatusReady, StatusInProgress, StatusReview}
	}
	return BoardStatuses
}

// List returns the profile's tasks by column, then position, then id, at most
// f.Limit of them (500 when it is not above 0, never more than 2,000), from the
// statuses named or, when none is, the five columns for the operator and ready,
// in_progress and review for an agent. A capture may not read and an actor that
// was never set is refused, before the filter is looked at. The profile is not
// checked (an unknown one lists nothing): resolve it with Profile first.
func (s *Store) List(ctx context.Context, profileID string, f Filter, actor Actor) ([]Task, error) {
	// Who is asking comes before what is asked, as in Add.
	switch actor.Kind {
	case Human, Agent:
	case Capture:
		return nil, operatorOnly("list tasks")
	default:
		return nil, invalid("unknown actor")
	}
	if err := bound(actor, 0); err != nil {
		return nil, err
	}
	// The names are parsed and the parsed statuses are queried: "progress" and
	// "In-Progress" are accepted as in_progress, so they must find it. A status
	// named twice is asked for once, which keeps the query to six slots however
	// long the caller's list is.
	statuses := make([]Status, 0, len(BoardStatuses)+1)
	for _, name := range f.Statuses {
		st, err := ParseStatus(string(name))
		if err != nil {
			return nil, err
		}
		if !slices.Contains(statuses, st) {
			statuses = append(statuses, st)
		}
	}
	if len(statuses) == 0 {
		statuses = defaultStatuses(actor)
		// An agent the operator let see the board also sees the Inbox in the default list.
		if s.agentMay(ctx, profileID, actor, func(a AgentAccess) bool { return a.View }) {
			statuses = BoardStatuses
		}
	}
	if f.Source != "" && !validSource(f.Source) {
		return nil, invalid("unknown source %q", echo(f.Source))
	}
	n := f.Limit
	if n <= 0 {
		n = DefaultListLimit
	}
	if n > MaxListLimit {
		n = MaxListLimit
	}
	q := `SELECT ` + taskCols + ` FROM tasks WHERE profile_id = ? AND session_id IS NULL AND status IN (` + placeholders(len(statuses)) + `)`
	args := []any{profileID}
	for _, st := range statuses {
		args = append(args, string(st))
	}
	if f.Source != "" {
		q += ` AND source_kind = ?`
		args = append(args, f.Source)
	}
	if f.ClaimedBy != "" {
		q += ` AND claimed_by = ?`
		args = append(args, f.ClaimedBy)
	}
	if f.Stale {
		q += ` AND status = 'in_progress' AND claimed_by <> '' AND claim_until <= ?`
		args = append(args, s.stamp())
	}
	q += ` ORDER BY ` + statusOrder + `, position, id LIMIT ?`
	args = append(args, n)
	var out []Task
	err := s.snapshot(ctx, func(x dbx) error {
		var err error
		out, err = s.queryTasks(ctx, x, q, args...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Board returns the whole board in one snapshot: the revision, the counts and
// the five columns. The Done column is cut to doneLimit cards when it is above 0.
// It is the operator's whole-board read, and the store holds it so: the board shows
// the Inbox, which spec 5.1 withholds from agents, so anyone but the operator (an
// agent, a capture, an actor that was never set) is refused with ErrOperatorOnly,
// before the profile is looked up or the database is read. It checks the profile
// (an unknown one is ErrInvalid).
func (s *Store) Board(ctx context.Context, profileID string, doneLimit int, actor Actor) (Board, error) {
	// Who is asking comes before what is asked, as in Edit.
	if actor.Kind != Human {
		if err := bound(actor, 0); err != nil {
			return Board{}, err
		}
		if !s.agentMay(ctx, profileID, actor, func(a AgentAccess) bool { return a.View }) {
			return Board{}, operatorOnly("show the board")
		}
	}
	b := Board{Tasks: map[Status][]Task{}}
	err := s.snapshot(ctx, func(x dbx) error {
		var err error
		if b.Profile, err = s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		if b.Rev, err = s.revOf(ctx, x, profileID); err != nil {
			return err
		}
		if b.Counts, err = s.countsOf(ctx, x, profileID); err != nil {
			return err
		}
		for _, st := range BoardStatuses {
			q := `SELECT ` + taskCols + ` FROM tasks WHERE profile_id = ? AND session_id IS NULL AND status = ? ORDER BY position, id`
			args := []any{profileID, string(st)}
			if st == StatusDone && doneLimit > 0 {
				q += ` LIMIT ?`
				args = append(args, doneLimit)
			}
			if b.Tasks[st], err = s.queryTasks(ctx, x, q, args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Board{}, err
	}
	return b, nil
}
