### Task 1: Migration 062 and the schema

**Files:**
- Create: `data/migrations/062_tasks.sql`, `internal/tasks/doc.go`
- Test: `internal/tasks/schema_test.go`

**Interfaces:**
- Consumes: `testdb.Open(t) *storage.Database` (field `DB *sql.DB`); the migrated database already has the profile `default`.
- Produces: the tables `tasks`, `task_events`, `task_board_rev` that every later task reads and writes.

- [ ] **Step 1: Re-check that 062 is free**

Run (three separate calls): `ls data/migrations | tail -3`, then `git fetch origin master`, then `git ls-tree --name-only origin/master data/migrations/ | tail -3`. Also list `data/migrations` in the other checkouts: `/Users/morteza/Desktop/monoes/mono-agent`, `/Users/morteza/Desktop/monoes/mono-agent-freebuff`, `/Users/morteza/Desktop/monoes/mono-agent-kilo`, `/Users/morteza/Desktop/monoes/mono-agent/.claude/worktrees/feat+monoes-account-gate` (use `ls` with the full path).
Expected: the highest number is 061 everywhere. If any checkout or `origin/master` has a `062_*`, stop and tell the lead: the number must change here and in the spec.

- [ ] **Step 2: Write the failing test**

Create `internal/tasks/schema_test.go`:

```go
package tasks

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/testdb"
)

const rowTime = "2026-10-05T12:00:00Z"

// insertRow writes a minimal tasks row straight into the table.
func insertRow(db *sql.DB, profile, status, clientID string) (int64, error) {
	var cid any
	if clientID != "" {
		cid = clientID
	}
	res, err := db.Exec(`INSERT INTO tasks (profile_id, title, status, position, client_id, created_at, updated_at)
		VALUES (?, 'x', ?, 1, ?, ?, ?)`, profile, status, cid, rowTime, rowTime)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func TestMigrationCreatesTheBoardTables(t *testing.T) {
	db := testdb.Open(t).DB
	for _, table := range []string{"tasks", "task_events", "task_board_rev"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s: count %d, err %v", table, n, err)
		}
	}
}

func TestATaskNeedsAnExistingProfile(t *testing.T) {
	db := testdb.Open(t).DB
	if _, err := insertRow(db, "default", "inbox", ""); err != nil {
		t.Fatalf("a task in the default profile: %v", err)
	}
	_, err := insertRow(db, "no-such-profile", "inbox", "")
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("a task in an unknown profile: err %v, want a foreign key failure", err)
	}
	if _, err := db.Exec(`INSERT INTO task_board_rev (profile_id, rev) VALUES ('no-such-profile', 1)`); err == nil {
		t.Error("a revision row for an unknown profile was accepted")
	}
}

func TestDeletingAProfileDeletesItsBoard(t *testing.T) {
	db := testdb.Open(t).DB
	if _, err := db.Exec(`INSERT INTO profiles (id, name) VALUES ('p1', 'P1')`); err != nil {
		t.Fatal(err)
	}
	id, err := insertRow(db, "p1", "ready", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_events (task_id, at, actor, kind) VALUES (?, ?, 'you', 'created')`, id, rowTime); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_board_rev (profile_id, rev) VALUES ('p1', 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM profiles WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"tasks", "task_events", "task_board_rev"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s after deleting the profile: %d rows, err %v", table, n, err)
		}
	}
}

func TestColumnChecksAndTheClientIDIndex(t *testing.T) {
	db := testdb.Open(t).DB
	if _, err := insertRow(db, "default", "bogus", ""); err == nil || !strings.Contains(err.Error(), "CHECK") {
		t.Errorf("status bogus: err %v, want a CHECK failure", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (profile_id, title, source_kind, position, created_at, updated_at)
		VALUES ('default', 'x', 'carrier-pigeon', 1, ?, ?)`, rowTime, rowTime); err == nil {
		t.Error("an unknown source kind was accepted")
	}
	if _, err := db.Exec(`INSERT INTO profiles (id, name) VALUES ('p2', 'P2')`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		profile, client string
		wantErr         bool
	}{
		{"default", "c-1", false},
		{"default", "c-1", true}, // the same key in the same profile
		{"p2", "c-1", false},     // the same key in another profile
		{"default", "", false},   // no key: any number of them
		{"default", "", false},
	} {
		if _, err := insertRow(db, c.profile, "inbox", c.client); (err != nil) != c.wantErr {
			t.Errorf("profile %s client %q: err %v, want error %v", c.profile, c.client, err, c.wantErr)
		}
	}
}
```

- [ ] **Step 3: Run the tests to see them fail**

Run: `go test ./internal/tasks/ -run 'TestMigration|TestATask|TestDeleting|TestColumnChecks' -count=1`
Expected: FAIL (`no such table: tasks`; the package has no Go source yet, which is fine for a test-only run).

- [ ] **Step 4: Write the package doc and the migration**

Create `internal/tasks/doc.go`:

```go
// Package tasks is the personal task board: tasks that sit in one profile,
// move through five columns and are worked by the operator and by agents.
//
// Every rule (who may do what, claims and leases, ordering, limits) lives
// here and nowhere else; the CLI, the MCP tools, the extension bridge and the
// app are thin callers. Every method takes the profile id: there is no task
// outside a profile and no query without one.
//
// Spec: docs/mastermind/specs/2026-10-05-task-board-design.md.
package tasks
```

Create `data/migrations/062_tasks.sql` (the splitter skips `--` comment lines, but keep them plain):

```sql
-- The personal task board (docs/mastermind/specs/2026-10-05-task-board-design.md,
-- section 4.2). A task always sits in one profile: deleting the profile deletes
-- its board. Times are fixed-width UTC text, so comparing text compares times.
-- position orders cards inside a column. client_id is an idempotency key from a
-- capture surface. claimed_by and claim_until are empty unless an agent holds
-- the task.
CREATE TABLE IF NOT EXISTS tasks (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id   TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    notes        TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'inbox'
                 CHECK (status IN ('inbox','ready','in_progress','review','done','archived')),
    position     INTEGER NOT NULL,
    source_kind  TEXT NOT NULL DEFAULT 'cli'
                 CHECK (source_kind IN ('cli','app','chrome','os','agent')),
    source_url   TEXT NOT NULL DEFAULT '',
    source_title TEXT NOT NULL DEFAULT '',
    source_app   TEXT NOT NULL DEFAULT '',
    client_id    TEXT,
    claimed_by   TEXT NOT NULL DEFAULT '',
    claim_until  TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_board ON tasks(profile_id, status, position);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_client ON tasks(profile_id, client_id) WHERE client_id IS NOT NULL;

-- One row per change of a task, written in the same transaction as the change.
CREATE TABLE IF NOT EXISTS task_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    at          TEXT NOT NULL,
    actor       TEXT NOT NULL,
    kind        TEXT NOT NULL,
    from_status TEXT NOT NULL DEFAULT '',
    to_status   TEXT NOT NULL DEFAULT '',
    note        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_task_events_task ON task_events(task_id, id);

-- One counter per profile, bumped by every write transaction, so a watcher
-- detects a change with a primary key read.
CREATE TABLE IF NOT EXISTS task_board_rev (
    profile_id TEXT PRIMARY KEY REFERENCES profiles(id) ON DELETE CASCADE,
    rev        INTEGER NOT NULL
);
```

- [ ] **Step 5: Run the tests to see them pass**

Run: `go test ./internal/tasks/ -run 'TestMigration|TestATask|TestDeleting|TestColumnChecks' -count=1`
Expected: PASS (four tests). If `TestATaskNeedsAnExistingProfile` fails because the insert succeeded, foreign keys are off on that connection: stop and report, do not weaken the test.

- [ ] **Step 6: Commit**

```
git add data/migrations/062_tasks.sql internal/tasks/doc.go internal/tasks/schema_test.go
```
then
```
git commit -m "feat(tasks): migration 062, the board tables keyed to profiles" -m "Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>"
```

