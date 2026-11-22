### Task 4: Reading a board

**Files:**
- Create: `internal/tasks/read.go`
- Test: `internal/tasks/read_test.go`

**Interfaces:**
- Consumes: Task 3's `Store`, `dbx`, `s.snapshot`, `s.profileOf`, `s.getTx`, `s.scanTask`, `s.attachLast`, `taskCols`, `placeholders`.
- Produces:
  - `(*Store).Rev(ctx, profileID string) (int64, error)`
  - `(*Store).Counts(ctx, profileID string) (Counts, error)`
  - `(*Store).Get(ctx, profileID string, id int64) (Task, []Event, error)` (events oldest first, never nil)
  - `(*Store).List(ctx, profileID string, f Filter, actor Actor) ([]Task, error)` (never nil)
  - `(*Store).Board(ctx, profileID string, doneLimit int) (Board, error)` (every board column present, never nil)
  - unexported: `s.revOf`, `s.countsOf`, `s.queryTasks`, `defaultStatuses`, `statusOrder`.
- Test helper added here: `seedRow(t, db, profile, status string) int64`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tasks/read_test.go`:

```go
package tasks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// seedRow inserts a row of the given status straight into the table.
func seedRow(t *testing.T, db *sql.DB, profile, status string) int64 {
	t.Helper()
	id, err := insertRow(db, profile, status, "")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGetReturnsEventsOldestFirstAndNeverNil(t *testing.T) {
	s, db, _ := newTestStore(t)
	task := mustAdd(t, s, "default", "a", false)
	if _, err := db.Exec(`INSERT INTO task_events (task_id, at, actor, kind, note) VALUES (?, ?, 'bot', 'comment', 'later')`, task.ID, rowTime); err != nil {
		t.Fatal(err)
	}
	got, events, err := s.Get(bg, "default", task.ID)
	if err != nil || got.ID != task.ID {
		t.Fatalf("get: %+v, %v", got, err)
	}
	if len(events) != 2 || events[0].Kind != "created" || events[1].Kind != "comment" || events[1].Note != "later" {
		t.Errorf("events: %+v", events)
	}
	if got.LastEvent == nil || got.LastEvent.Kind != "comment" {
		t.Errorf("last event: %+v", got.LastEvent)
	}
	_, none, err := s.Get(bg, "default", seedRow(t, db, "default", "inbox"))
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("events of a task with none: %v, %v", none, err)
	}
	if _, _, err := s.Get(bg, "default", 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v, want ErrNotFound", err)
	}
}

func TestListDefaultsDependOnTheCaller(t *testing.T) {
	s, db, _ := newTestStore(t)
	for _, st := range []string{"inbox", "ready", "in_progress", "review", "done", "archived"} {
		seedRow(t, db, "default", st)
	}
	names := func(f Filter, a Actor) string {
		ts, err := s.List(bg, "default", f, a)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, task := range ts {
			out = append(out, string(task.Status))
		}
		return strings.Join(out, ",")
	}
	if got := names(Filter{}, human); got != "inbox,ready,in_progress,review,done" {
		t.Errorf("the operator sees %q", got)
	}
	if got := names(Filter{}, bot("b")); got != "ready,in_progress,review" {
		t.Errorf("an agent sees %q", got)
	}
	if got := names(Filter{Statuses: []Status{StatusInbox, StatusArchived}}, bot("b")); got != "inbox,archived" {
		t.Errorf("naming statuses overrides the default, for an agent too: %q", got)
	}
}

func TestListFilters(t *testing.T) {
	s, db, c := newTestStore(t)
	chrome := Actor{Kind: Capture, Name: SourceChrome}
	if _, _, err := s.Add(bg, "default", AddInput{Title: "from chrome", SourceKind: SourceChrome}, chrome); err != nil {
		t.Fatal(err)
	}
	mustAdd(t, s, "default", "manual", false)
	past := c.t.Add(-time.Minute).Format(timeFmt)
	future := c.t.Add(time.Hour).Format(timeFmt)
	stale, live := seedRow(t, db, "default", "in_progress"), seedRow(t, db, "default", "in_progress")
	if _, err := db.Exec(`UPDATE tasks SET claimed_by = 'old', claim_until = ? WHERE id = ?`, past, stale); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET claimed_by = 'live', claim_until = ? WHERE id = ?`, future, live); err != nil {
		t.Fatal(err)
	}
	only := func(f Filter) []Task {
		ts, err := s.List(bg, "default", f, human)
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	if ts := only(Filter{Source: SourceChrome}); len(ts) != 1 || ts[0].Title != "from chrome" {
		t.Errorf("source filter: %+v", ts)
	}
	if ts := only(Filter{ClaimedBy: "live"}); len(ts) != 1 || ts[0].ID != live || ts[0].Claim == nil || ts[0].Claim.Stale {
		t.Errorf("claimed-by filter: %+v", ts)
	}
	if ts := only(Filter{Stale: true}); len(ts) != 1 || ts[0].ID != stale || !ts[0].Claim.Stale {
		t.Errorf("stale filter: %+v", ts)
	}
	if ts := only(Filter{Limit: 2}); len(ts) != 2 {
		t.Errorf("limit: %d tasks", len(ts))
	}
	if _, err := s.List(bg, "default", Filter{Source: "pigeon"}, human); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown source: %v", err)
	}
	if _, err := s.List(bg, "default", Filter{Statuses: []Status{"bogus"}}, human); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown status: %v", err)
	}
}

func TestListOrdersByColumnThenPosition(t *testing.T) {
	s, _, _ := newTestStore(t)
	r1 := mustAdd(t, s, "default", "r1", true)
	i1 := mustAdd(t, s, "default", "i1", false)
	r2 := mustAdd(t, s, "default", "r2", true)
	i2 := mustAdd(t, s, "default", "i2", false)
	ts, err := s.List(bg, "default", Filter{}, human)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, task := range ts {
		got = append(got, task.ID)
	}
	want := []int64{i2.ID, i1.ID, r1.ID, r2.ID} // inbox newest first, then the ready queue
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBoardCarriesFiveColumnsCountsAndTheRevision(t *testing.T) {
	s, db, c := newTestStore(t)
	empty, err := s.Board(bg, "default", 0)
	if err != nil || empty.Profile.ID != "default" || empty.Rev != 0 {
		t.Fatalf("empty board: %+v, %v", empty, err)
	}
	raw, _ := json.Marshal(empty)
	for _, col := range []string{"inbox", "ready", "in_progress", "review", "done"} {
		if !strings.Contains(string(raw), `"`+col+`":[]`) {
			t.Errorf("column %s is not an empty array in %s", col, raw)
		}
	}

	mustAdd(t, s, "default", "i1", false)
	mustAdd(t, s, "default", "i2", false)
	mustAdd(t, s, "default", "r1", true)
	for i := 0; i < 3; i++ {
		seedRow(t, db, "default", "done")
	}
	stale := seedRow(t, db, "default", "in_progress")
	if _, err := db.Exec(`UPDATE tasks SET claimed_by = 'old', claim_until = ? WHERE id = ?`, c.t.Add(-time.Minute).Format(timeFmt), stale); err != nil {
		t.Fatal(err)
	}
	b, err := s.Board(bg, "default", 2)
	if err != nil {
		t.Fatal(err)
	}
	want := Counts{Inbox: 2, Ready: 1, InProgress: 1, Done: 3, Stale: 1}
	if b.Counts != want {
		t.Errorf("counts %+v, want %+v", b.Counts, want)
	}
	if len(b.Tasks[StatusDone]) != 2 || len(b.Tasks[StatusInbox]) != 2 || len(b.Tasks[StatusReady]) != 1 {
		t.Errorf("columns: done %d inbox %d ready %d", len(b.Tasks[StatusDone]), len(b.Tasks[StatusInbox]), len(b.Tasks[StatusReady]))
	}
	if b.Rev != 3 {
		t.Errorf("revision %d, want 3 (one per add)", b.Rev)
	}
}

func TestEveryReadIsScopedToItsProfile(t *testing.T) {
	s, db, _ := newTestStore(t)
	other := addProfile(t, db, "p2")
	mine := mustAdd(t, s, "default", "mine", true)
	theirs := mustAdd(t, s, other, "theirs", true)
	if _, _, err := s.Get(bg, "default", theirs.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another profile's task by id: %v, want ErrNotFound", err)
	}
	if _, _, err := s.Get(bg, other, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another profile's task by id: %v, want ErrNotFound", err)
	}
	if ts, _ := s.List(bg, "default", Filter{}, human); len(ts) != 1 || ts[0].ID != mine.ID {
		t.Errorf("list: %+v", ts)
	}
	if b, _ := s.Board(bg, other, 0); len(b.Tasks[StatusReady]) != 1 || b.Tasks[StatusReady][0].ID != theirs.ID {
		t.Errorf("board of the other profile: %+v", b.Tasks)
	}
	if cn, _ := s.Counts(bg, "default"); cn.Ready != 1 {
		t.Errorf("counts: %+v", cn)
	}
	if _, err := s.Board(bg, "no-such-profile", 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("board of an unknown profile: %v, want ErrInvalid", err)
	}
}

func TestRevMovesOnWritesNotOnReads(t *testing.T) {
	s, _, _ := newTestStore(t)
	rev := func() int64 {
		r, err := s.Rev(bg, "default")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if rev() != 0 {
		t.Fatal("a new board starts at revision 0")
	}
	task := mustAdd(t, s, "default", "a", false)
	_, _, _ = s.Get(bg, "default", task.ID)
	_, _ = s.List(bg, "default", Filter{}, human)
	_, _ = s.Board(bg, "default", 0)
	if rev() != 1 {
		t.Errorf("revision %d after one add and some reads, want 1", rev())
	}
	mustAdd(t, s, "default", "b", false)
	if rev() != 2 {
		t.Errorf("revision %d after two adds, want 2", rev())
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/tasks/ -run 'TestGet|TestList|TestBoard|TestEveryRead|TestRev' -count=1`
Expected: FAIL to compile (`s.Get undefined`, `s.List undefined`, ...).

- [ ] **Step 3: Write `internal/tasks/read.go`**

```go
package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// statusOrder sorts a query by column, in board order.
const statusOrder = `CASE status WHEN 'inbox' THEN 0 WHEN 'ready' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'review' THEN 3 WHEN 'done' THEN 4 ELSE 5 END`

// Rev is the profile's board revision: 0 until its first write.
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

// Counts returns the profile's cards per column and its stale claims.
func (s *Store) Counts(ctx context.Context, profileID string) (Counts, error) {
	return s.countsOf(ctx, s.db, profileID)
}

func (s *Store) countsOf(ctx context.Context, x dbx, profileID string) (Counts, error) {
	var c Counts
	rows, err := x.QueryContext(ctx, `SELECT status, COUNT(*) FROM tasks WHERE profile_id = ? GROUP BY status`, profileID)
	if err != nil {
		return c, fmt.Errorf("tasks: counting: %w", err)
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return c, fmt.Errorf("tasks: counting: %w", err)
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
		rows.Close()
		return c, fmt.Errorf("tasks: counting: %w", err)
	}
	rows.Close()
	err = x.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE profile_id = ? AND status = 'in_progress' AND claimed_by <> '' AND claim_until <= ?`,
		profileID, s.stamp()).Scan(&c.Stale)
	if err != nil {
		return c, fmt.Errorf("tasks: counting stale claims: %w", err)
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
	out := []Task{}
	for rows.Next() {
		t, err := s.scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("tasks: reading tasks: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("tasks: reading tasks: %w", err)
	}
	rows.Close()
	return out, s.attachLast(ctx, x, out)
}

// Get returns a task of the profile with its events, oldest first.
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
			e.At, _ = time.Parse(timeFmt, at)
			events = append(events, e)
		}
		return rows.Err()
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

// List returns the profile's tasks, by column and then by position.
func (s *Store) List(ctx context.Context, profileID string, f Filter, actor Actor) ([]Task, error) {
	statuses := f.Statuses
	if len(statuses) == 0 {
		statuses = defaultStatuses(actor)
	}
	for _, st := range statuses {
		if _, err := ParseStatus(string(st)); err != nil {
			return nil, err
		}
	}
	if f.Source != "" && !validSource(f.Source) {
		return nil, invalid("unknown source %q", f.Source)
	}
	n := f.Limit
	if n <= 0 {
		n = DefaultListLimit
	}
	if n > MaxListLimit {
		n = MaxListLimit
	}
	q := `SELECT ` + taskCols + ` FROM tasks WHERE profile_id = ? AND status IN (` + placeholders(len(statuses)) + `)`
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
	return s.queryTasks(ctx, s.db, q, args...)
}

// Board returns the whole board in one snapshot: the revision, the counts and
// the five columns. The Done column is cut to doneLimit cards when it is above 0.
func (s *Store) Board(ctx context.Context, profileID string, doneLimit int) (Board, error) {
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
			q := `SELECT ` + taskCols + ` FROM tasks WHERE profile_id = ? AND status = ? ORDER BY position, id`
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
	return b, err
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `gofmt -l internal/tasks` then `go vet ./internal/tasks/` then `go test ./internal/tasks/ -count=1`
Expected: nothing from `gofmt`, vet clean, PASS.

- [ ] **Step 5: Commit**

```
git add internal/tasks/read.go internal/tasks/read_test.go
```
then
```
git commit -m "feat(tasks): read a board: Get, List, Board, Rev, Counts" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

