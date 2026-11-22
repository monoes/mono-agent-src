### Task 3: The store core and `Add`

**Files:**
- Create: `internal/tasks/store.go`
- Test: `internal/tasks/helpers_test.go`, `internal/tasks/store_test.go`

**Interfaces:**
- Consumes: everything Task 2 produced; the tables of Task 1.
- Produces: `Store`, `NewStore(*sql.DB) *Store`, and these methods and helpers that later tasks call exactly as written:
  - `(*Store).Profile(ctx, profileID string) (Profile, error)`
  - `(*Store).Add(ctx, profileID string, in AddInput, actor Actor) (Task, bool, error)` (the bool is "created")
  - unexported, used by `read.go`, `ops.go` and `claims.go`: `s.now func() time.Time`, `s.stamp() string`, `s.tx(ctx, fn func(x dbx) error) error` (BEGIN IMMEDIATE), `s.snapshot(ctx, fn func(x dbx) error) error` (one read transaction), `dbx`, `s.profileOf(ctx, x, id) (Profile, error)`, `s.event(ctx, x, taskID int64, at, actor, kind, from, to, note string) error`, `s.bump(ctx, x, profileID string) error`, `s.edgePosition(ctx, x, profileID string, status Status, top bool, except int64) (int64, error)`, `atTop(Status) bool`, `placeholders(n int) string`, `taskCols`, `s.scanTask(row) (Task, error)`, `s.getTx(ctx, x, profileID string, id int64) (Task, error)`, `s.attachLast(ctx, x, []Task) error`.
- Test helpers (package-level in `helpers_test.go`, used by every later test file): `bg`, `human`, `bot(name)`, `clock` with `newClock()`, `now()`, `advance(d)`, `newTestStore(t) (*Store, *sql.DB, *clock)`, `addProfile(t, db, id) string`, `mustAdd(t, s, profile, title string, ready bool) Task`, `countWhere(t, db, table, where string, args...) int`, `seedTasks(t, db, profile string, n int)`.

- [ ] **Step 1: Write the test helpers and the failing tests**

Create `internal/tasks/helpers_test.go`:

```go
package tasks

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/testdb"
)

var (
	bg    = context.Background()
	human = Actor{Kind: Human}
)

func bot(name string) Actor { return Actor{Kind: Agent, Name: name} }

// clock is a time the test controls, for leases and the hourly limit.
type clock struct{ t time.Time }

func newClock() *clock                   { return &clock{t: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)} }
func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestStore is a store over a migrated database of its own (the profile
// "default" exists) with a clock the test controls.
func newTestStore(t *testing.T) (*Store, *sql.DB, *clock) {
	t.Helper()
	db := testdb.Open(t).DB
	s := NewStore(db)
	c := newClock()
	s.now = c.now
	return s, db, c
}

// addProfile inserts a profile and returns its id.
func addProfile(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO profiles (id, name) VALUES (?, ?)`, id, "Profile "+id); err != nil {
		t.Fatal(err)
	}
	return id
}

// mustAdd adds an operator task: to Inbox, or to Ready with ready.
func mustAdd(t *testing.T, s *Store, profile, title string, ready bool) Task {
	t.Helper()
	task, _, err := s.Add(bg, profile, AddInput{Title: title, Ready: ready}, human)
	if err != nil {
		t.Fatalf("add %q: %v", title, err)
	}
	return task
}

// countWhere counts the rows of a table that match a where clause.
func countWhere(t *testing.T, db *sql.DB, table, where string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedTasks inserts n Inbox rows straight into the table, fast.
func seedTasks(t *testing.T, db *sql.DB, profile string, n int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := tx.Exec(`INSERT INTO tasks (profile_id, title, position, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			profile, fmt.Sprintf("seed %d", i), i, rowTime, rowTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
```

Create `internal/tasks/store_test.go`:

```go
package tasks

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAddLandsInInboxAndRecordsIt(t *testing.T) {
	s, db, c := newTestStore(t)
	task, created, err := s.Add(bg, "default", AddInput{Title: "  Fix   the flaky test  "}, human)
	if err != nil || !created {
		t.Fatalf("add: created %v, err %v", created, err)
	}
	if task.ID == 0 || task.Status != StatusInbox || task.Title != "Fix the flaky test" || task.Source.Kind != SourceCLI || task.ProfileID != "default" {
		t.Errorf("task: %+v", task)
	}
	if !task.CreatedAt.Equal(c.t) || task.Claim != nil {
		t.Errorf("times or claim: %+v", task)
	}
	if task.LastEvent == nil || task.LastEvent.Kind != "created" || task.LastEvent.Actor != "you" {
		t.Errorf("last event: %+v", task.LastEvent)
	}
	if n := countWhere(t, db, "task_events", "task_id = ? AND kind = 'created' AND to_status = 'inbox'", task.ID); n != 1 {
		t.Errorf("created events: %d", n)
	}
	var rev int
	if err := db.QueryRow(`SELECT rev FROM task_board_rev WHERE profile_id = 'default'`).Scan(&rev); err != nil || rev != 1 {
		t.Errorf("revision %d, err %v", rev, err)
	}
}

func TestAddReadyIsForTheOperatorOnly(t *testing.T) {
	s, _, _ := newTestStore(t)
	task, _, err := s.Add(bg, "default", AddInput{Title: "go", Ready: true}, human)
	if err != nil || task.Status != StatusReady {
		t.Fatalf("operator with Ready: %+v, %v", task, err)
	}
	for _, a := range []Actor{bot("b"), {Kind: Capture, Name: SourceOS}} {
		if _, _, err := s.Add(bg, "default", AddInput{Title: "go", Ready: true, SourceKind: SourceOS}, a); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%+v adding to Ready: %v, want ErrOperatorOnly", a, err)
		}
	}
}

func TestSourceKindRules(t *testing.T) {
	cases := []struct {
		name      string
		actor     Actor
		requested string
		want      string // "" means refused
	}{
		{"operator default", human, "", SourceCLI},
		{"operator cli", human, SourceCLI, SourceCLI},
		{"operator app", human, SourceApp, SourceApp},
		{"operator may not claim chrome", human, SourceChrome, ""},
		{"agent default", bot("b"), "", SourceAgent},
		{"agent with the cli default", bot("b"), SourceCLI, SourceAgent},
		{"agent may not claim os", bot("b"), SourceOS, ""},
		{"agent may not claim chrome", bot("b"), SourceChrome, ""},
		{"os capture", Actor{Kind: Capture, Name: SourceOS}, SourceOS, SourceOS},
		{"chrome capture by name", Actor{Kind: Capture, Name: SourceChrome}, "", SourceChrome},
		{"capture claiming cli", Actor{Kind: Capture, Name: SourceOS}, SourceCLI, ""},
	}
	s, _, _ := newTestStore(t)
	for _, c := range cases {
		task, _, err := s.Add(bg, "default", AddInput{Title: "t", SourceKind: c.requested}, c.actor)
		if c.want == "" {
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: err %v, want ErrInvalid", c.name, err)
			}
			continue
		}
		if err != nil || task.Source.Kind != c.want {
			t.Errorf("%s: kind %q, err %v, want %q", c.name, task.Source.Kind, err, c.want)
		}
	}
}

func TestAddRefusesAnUnknownProfile(t *testing.T) {
	s, db, _ := newTestStore(t)
	if _, _, err := s.Add(bg, "no-such-profile", AddInput{Title: "t"}, human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown profile: %v, want ErrInvalid", err)
	}
	if n := countWhere(t, db, "tasks", "1 = 1"); n != 0 {
		t.Errorf("%d tasks were written", n)
	}
}

func TestAddRefusesTextThatCleansToNothing(t *testing.T) {
	s, db, _ := newTestStore(t)
	for _, in := range []AddInput{
		{Text: " \x1b\x00 \t\n"},
		{Title: "\x07 \x1b"},
		{Text: "\U000E0049\U0000202e"},
		{},
	} {
		if _, _, err := s.Add(bg, "default", in, human); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: err %v, want ErrInvalid", in, err)
		}
	}
	if n := countWhere(t, db, "tasks", "1 = 1"); n != 0 {
		t.Errorf("%d empty tasks were stored", n)
	}
}

func TestAddCleansWhatItStores(t *testing.T) {
	s, _, _ := newTestStore(t)
	chrome := Actor{Kind: Capture, Name: SourceChrome}
	task, _, err := s.Add(bg, "default", AddInput{
		Text:        "Reply to Sam\x1b[31m\nabout the invoice",
		SourceKind:  SourceChrome,
		SourceURL:   "https://user:pw@example.com/mail?id=7",
		SourceTitle: "  Inbox \n (3)  ",
		SourceApp:   "Google Chrome",
	}, chrome)
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Reply to Sam[31m" || strings.ContainsRune(task.Notes, 0x1b) {
		t.Errorf("title %q notes %q: the escape byte must be gone", task.Title, task.Notes)
	}
	if !strings.Contains(task.Notes, "about the invoice") {
		t.Errorf("notes %q must keep the whole text", task.Notes)
	}
	if task.Source.URL != "https://example.com/mail?id=7" || task.Source.Title != "Inbox (3)" || task.Source.App != "Google Chrome" {
		t.Errorf("source %+v", task.Source)
	}
}

func TestClientIDMakesAddIdempotent(t *testing.T) {
	s, db, _ := newTestStore(t)
	other := addProfile(t, db, "p2")
	chrome := Actor{Kind: Capture, Name: SourceChrome}
	in := AddInput{Title: "once", ClientID: "c-1", SourceKind: SourceChrome}
	first, created, err := s.Add(bg, "default", in, chrome)
	if err != nil || !created {
		t.Fatalf("first: created %v, err %v", created, err)
	}
	second, created, err := s.Add(bg, "default", in, chrome)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("second: %+v created %v err %v, want the first task back", second, created, err)
	}
	if n := countWhere(t, db, "tasks", "profile_id = 'default'"); n != 1 {
		t.Errorf("%d tasks in the profile", n)
	}
	third, created, err := s.Add(bg, other, in, chrome)
	if err != nil || !created || third.ID == first.ID {
		t.Errorf("the same key in another profile: %+v created %v err %v", third, created, err)
	}
	if _, _, err := s.Add(bg, "default", AddInput{Title: "x", ClientID: "bad id!", SourceKind: SourceChrome}, chrome); !errors.Is(err, ErrInvalid) {
		t.Errorf("a malformed client id: %v", err)
	}
}

func TestAgentsMayAddTwentyTasksAnHour(t *testing.T) {
	s, _, c := newTestStore(t)
	add := func() error {
		_, _, err := s.Add(bg, "default", AddInput{Title: "from an agent"}, bot("b"))
		return err
	}
	for i := 0; i < AgentTasksPerHour; i++ {
		if err := add(); err != nil {
			t.Fatalf("task %d: %v", i+1, err)
		}
	}
	if err := add(); !errors.Is(err, ErrLimit) {
		t.Fatalf("task 21: %v, want ErrLimit", err)
	}
	if _, _, err := s.Add(bg, "default", AddInput{Title: "operator"}, human); err != nil {
		t.Errorf("the operator is not limited: %v", err)
	}
	c.advance(61 * time.Minute)
	if err := add(); err != nil {
		t.Errorf("an hour later: %v", err)
	}
}

func TestAProfileHoldsAtMostTwoThousandOpenTasks(t *testing.T) {
	s, db, _ := newTestStore(t)
	seedTasks(t, db, "default", MaxOpenTasks)
	if _, _, err := s.Add(bg, "default", AddInput{Title: "one too many"}, human); !errors.Is(err, ErrLimit) {
		t.Fatalf("task 2001: %v, want ErrLimit", err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status = 'archived' WHERE id = (SELECT MIN(id) FROM tasks)`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Add(bg, "default", AddInput{Title: "room again"}, human); err != nil {
		t.Errorf("archived tasks do not count: %v", err)
	}
}

func TestNewTasksGoToTheTopOrBottomOfTheirColumn(t *testing.T) {
	s, _, _ := newTestStore(t)
	a, b := mustAdd(t, s, "default", "a", false), mustAdd(t, s, "default", "b", false)
	if b.Position >= a.Position {
		t.Errorf("inbox is newest first: a=%d b=%d", a.Position, b.Position)
	}
	r1, r2 := mustAdd(t, s, "default", "r1", true), mustAdd(t, s, "default", "r2", true)
	if r2.Position <= r1.Position {
		t.Errorf("ready is a queue: r1=%d r2=%d", r1.Position, r2.Position)
	}
}

func TestTaskTextIsStoredVerbatim(t *testing.T) {
	s, db, _ := newTestStore(t)
	title := `x'); DROP TABLE tasks; --`
	task := mustAdd(t, s, "default", title, false)
	var got string
	if err := db.QueryRow(`SELECT title FROM tasks WHERE id = ?`, task.ID).Scan(&got); err != nil || got != title {
		t.Fatalf("title %q, err %v", got, err)
	}
}
```

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/tasks/ -run 'TestAdd|TestSource|TestClientID|TestAgentsMay|TestAProfile|TestNewTasks|TestTaskText' -count=1`
Expected: FAIL to compile (`undefined: Store`, `undefined: NewStore`).

- [ ] **Step 3: Write `internal/tasks/store.go`**

```go
package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Store reads and writes boards. Every method takes the profile id.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// NewStore returns a Store over a migrated database.
func NewStore(db *sql.DB) *Store { return &Store{db: db, now: time.Now} }

// dbx is what *sql.DB and *sql.Conn both offer, so a helper runs in or out of
// a transaction.
type dbx interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) stamp() string { return s.now().UTC().Format(timeFmt) }

// tx runs fn in a BEGIN IMMEDIATE transaction on a connection of its own: the
// write lock is taken first, so processes that read and then write never
// interleave (the pattern of vault.Register). An error from fn rolls back.
func (s *Store) tx(ctx context.Context, fn func(x dbx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("tasks: connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("tasks: begin: %w", err)
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
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("tasks: commit: %w", err)
	}
	committed = true
	return nil
}

// snapshot runs fn in one read transaction, so what it reads agrees.
func (s *Store) snapshot(ctx context.Context, fn func(x dbx) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("tasks: connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return fmt.Errorf("tasks: begin: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	return fn(conn)
}

// Profile returns the profile or an ErrInvalid ("unknown profile").
func (s *Store) Profile(ctx context.Context, profileID string) (Profile, error) {
	return s.profileOf(ctx, s.db, profileID)
}

func (s *Store) profileOf(ctx context.Context, x dbx, profileID string) (Profile, error) {
	var p Profile
	err := x.QueryRowContext(ctx, `SELECT id, name FROM profiles WHERE id = ?`, profileID).Scan(&p.ID, &p.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, invalid("unknown profile %q", profileID)
	}
	if err != nil {
		return Profile{}, fmt.Errorf("tasks: reading the profile: %w", err)
	}
	return p, nil
}

// event writes one row of a task's history.
func (s *Store) event(ctx context.Context, x dbx, taskID int64, at, actor, kind, from, to, note string) error {
	_, err := x.ExecContext(ctx,
		`INSERT INTO task_events (task_id, at, actor, kind, from_status, to_status, note) VALUES (?,?,?,?,?,?,?)`,
		taskID, at, actor, kind, from, to, note)
	if err != nil {
		return fmt.Errorf("tasks: writing an event: %w", err)
	}
	return nil
}

// bump moves the profile's board revision; every write transaction does it once.
func (s *Store) bump(ctx context.Context, x dbx, profileID string) error {
	_, err := x.ExecContext(ctx,
		`INSERT INTO task_board_rev (profile_id, rev) VALUES (?, 1) ON CONFLICT(profile_id) DO UPDATE SET rev = rev + 1`, profileID)
	if err != nil {
		return fmt.Errorf("tasks: bumping the revision: %w", err)
	}
	return nil
}

// atTop says where a card new to a column goes: newest first where the order
// is chronological, at the end of a queue otherwise.
func atTop(st Status) bool { return st == StatusInbox || st == StatusReview || st == StatusDone }

// edgePosition is a position above every other card of the column (top) or
// below every one (bottom), ignoring the card except.
func (s *Store) edgePosition(ctx context.Context, x dbx, profileID string, status Status, top bool, except int64) (int64, error) {
	var lo, hi sql.NullInt64
	err := x.QueryRowContext(ctx,
		`SELECT MIN(position), MAX(position) FROM tasks WHERE profile_id = ? AND status = ? AND id <> ?`,
		profileID, string(status), except).Scan(&lo, &hi)
	if err != nil {
		return 0, fmt.Errorf("tasks: reading a column: %w", err)
	}
	switch {
	case !lo.Valid:
		return positionGap, nil
	case top:
		return lo.Int64 - positionGap, nil
	}
	return hi.Int64 + positionGap, nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

const taskCols = `id, profile_id, title, notes, status, position, source_kind, source_url, source_title, source_app, claimed_by, claim_until, created_at, updated_at`

type rowScanner interface{ Scan(dest ...any) error }

// scanTask reads a row of taskCols. A claim is stale when its lease has run out.
func (s *Store) scanTask(row rowScanner) (Task, error) {
	var t Task
	var status, claimedBy, until, created, updated string
	if err := row.Scan(&t.ID, &t.ProfileID, &t.Title, &t.Notes, &status, &t.Position,
		&t.Source.Kind, &t.Source.URL, &t.Source.Title, &t.Source.App, &claimedBy, &until, &created, &updated); err != nil {
		return Task{}, err
	}
	t.Status = Status(status)
	t.CreatedAt, _ = time.Parse(timeFmt, created)
	t.UpdatedAt, _ = time.Parse(timeFmt, updated)
	if claimedBy != "" {
		u, _ := time.Parse(timeFmt, until)
		t.Claim = &Claim{By: claimedBy, Until: u, Stale: !u.After(s.now())}
	}
	return t, nil
}

// getTx reads one task of the profile, with its last event.
func (s *Store) getTx(ctx context.Context, x dbx, profileID string, id int64) (Task, error) {
	t, err := s.scanTask(x.QueryRowContext(ctx, `SELECT `+taskCols+` FROM tasks WHERE profile_id = ? AND id = ?`, profileID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	if err != nil {
		return Task{}, fmt.Errorf("tasks: reading #%d: %w", id, err)
	}
	ts := []Task{t}
	if err := s.attachLast(ctx, x, ts); err != nil {
		return Task{}, err
	}
	return ts[0], nil
}

// attachLast fills LastEvent of every task with one query.
func (s *Store) attachLast(ctx context.Context, x dbx, ts []Task) error {
	if len(ts) == 0 {
		return nil
	}
	index := make(map[int64]int, len(ts))
	args := make([]any, 0, len(ts))
	for i, t := range ts {
		index[t.ID] = i
		args = append(args, t.ID)
	}
	rows, err := x.QueryContext(ctx,
		`SELECT task_id, actor, kind, at FROM task_events
		 WHERE id IN (SELECT MAX(id) FROM task_events WHERE task_id IN (`+placeholders(len(ts))+`) GROUP BY task_id)`, args...)
	if err != nil {
		return fmt.Errorf("tasks: reading last events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id int64
			le LastEvent
			at string
		)
		if err := rows.Scan(&id, &le.Actor, &le.Kind, &at); err != nil {
			return fmt.Errorf("tasks: reading last events: %w", err)
		}
		le.At, _ = time.Parse(timeFmt, at)
		ts[index[id]].LastEvent = &le
	}
	return rows.Err()
}

// sourceKindFor decides the stored source kind. The actor decides what is
// allowed: an agent's tasks are always agent tasks (it may not call itself a
// capture, which would skip the hourly limit), a capture names its surface, the
// operator is cli or app.
func sourceKindFor(actor Actor, requested string) (string, error) {
	switch actor.Kind {
	case Agent:
		if requested == "" || requested == SourceCLI || requested == SourceAgent {
			return SourceAgent, nil
		}
		return "", invalid("an agent's tasks are agent tasks: source %q is for captures", requested)
	case Capture:
		kind := requested
		if kind == "" {
			kind = actor.Name
		}
		if kind != SourceChrome && kind != SourceOS {
			return "", invalid("a capture comes from chrome or os, not %q", kind)
		}
		return kind, nil
	case Human:
		switch requested {
		case "", SourceCLI:
			return SourceCLI, nil
		case SourceApp:
			return SourceApp, nil
		}
		return "", invalid("source %q is for captures and agents: the operator's tasks come from cli or app", requested)
	}
	return "", invalid("unknown actor")
}

// checkAddLimits refuses a task that would pass the board's size or the hourly
// limit on agent-created tasks.
func (s *Store) checkAddLimits(ctx context.Context, x dbx, profileID, kind string) error {
	var open int
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE profile_id = ? AND status <> 'archived'`, profileID).Scan(&open); err != nil {
		return fmt.Errorf("tasks: counting tasks: %w", err)
	}
	if open >= MaxOpenTasks {
		return limit("this profile already has %d open tasks: archive some first", MaxOpenTasks)
	}
	if kind != SourceAgent {
		return nil
	}
	since := s.now().UTC().Add(-time.Hour).Format(timeFmt)
	var recent int
	if err := x.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE profile_id = ? AND source_kind = 'agent' AND created_at >= ?`, profileID, since).Scan(&recent); err != nil {
		return fmt.Errorf("tasks: counting agent tasks: %w", err)
	}
	if recent >= AgentTasksPerHour {
		return limit("agents may add %d tasks an hour to a profile: wait, or ask the operator", AgentTasksPerHour)
	}
	return nil
}

// Add creates a task in the profile's Inbox (or in Ready, for the operator who
// asks). With a ClientID it is idempotent: a second Add with the same key
// returns the task that exists and created is false.
func (s *Store) Add(ctx context.Context, profileID string, in AddInput, actor Actor) (Task, bool, error) {
	title, notes, err := deriveTitleNotes(in.Title, in.Notes, in.Text)
	if err != nil {
		return Task{}, false, err
	}
	status := StatusInbox
	if in.Ready {
		if actor.Kind != Human {
			return Task{}, false, operatorOnly("add a task straight to Ready")
		}
		status = StatusReady
	}
	kind, err := sourceKindFor(actor, in.SourceKind)
	if err != nil {
		return Task{}, false, err
	}
	if actor.Kind == Agent && actor.Name != "" && !nameRE.MatchString(actor.Name) {
		return Task{}, false, invalid("an agent name is 1-%d characters of letters, digits and . _ # @ : -", MaxNameLen)
	}
	clientID := strings.TrimSpace(in.ClientID)
	if clientID != "" && !clientIDRE.MatchString(clientID) {
		return Task{}, false, invalid("a client id is 1-%d characters of letters, digits, _ and -", MaxClientIDLen)
	}
	src := Source{
		Kind:  kind,
		URL:   cleanURL(in.SourceURL),
		Title: cutRunes(oneLine(in.SourceTitle), MaxSourceTitleRunes),
		App:   cutRunes(oneLine(in.SourceApp), MaxAppRunes),
	}

	var out Task
	var created bool
	err = s.tx(ctx, func(x dbx) error {
		if _, err := s.profileOf(ctx, x, profileID); err != nil {
			return err
		}
		if clientID != "" {
			var existing int64
			err := x.QueryRowContext(ctx, `SELECT id FROM tasks WHERE profile_id = ? AND client_id = ?`, profileID, clientID).Scan(&existing)
			if err == nil {
				out, err = s.getTx(ctx, x, profileID, existing)
				return err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("tasks: looking up the client id: %w", err)
			}
		}
		if err := s.checkAddLimits(ctx, x, profileID, kind); err != nil {
			return err
		}
		pos, err := s.edgePosition(ctx, x, profileID, status, atTop(status), 0)
		if err != nil {
			return err
		}
		var cid any
		if clientID != "" {
			cid = clientID
		}
		stamp := s.stamp()
		res, err := x.ExecContext(ctx,
			`INSERT INTO tasks (profile_id, title, notes, status, position, source_kind, source_url, source_title, source_app, client_id, created_at, updated_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			profileID, title, notes, string(status), pos, src.Kind, src.URL, src.Title, src.App, cid, stamp, stamp)
		if err != nil {
			return fmt.Errorf("tasks: adding a task: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("tasks: adding a task: %w", err)
		}
		if err := s.event(ctx, x, id, stamp, actor.Label(), "created", "", string(status), ""); err != nil {
			return err
		}
		if err := s.bump(ctx, x, profileID); err != nil {
			return err
		}
		out, err = s.getTx(ctx, x, profileID, id)
		created = err == nil
		return err
	})
	if err != nil {
		return Task{}, false, err
	}
	return out, created, nil
}
```

- [ ] **Step 4: Run the tests to see them pass**

Run: `gofmt -l internal/tasks` then `go vet ./internal/tasks/` then `go test ./internal/tasks/ -count=1`
Expected: `gofmt` prints nothing (if it lists files, run `gofmt -w` on exactly those: the aligned one-line functions in `helpers_test.go` are the usual cause), vet is clean, all tests PASS.

- [ ] **Step 5: Commit**

```
git add internal/tasks/store.go internal/tasks/helpers_test.go internal/tasks/store_test.go
```
then
```
git commit -m "feat(tasks): the store core and Add, with the source, limit and idempotency rules" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

