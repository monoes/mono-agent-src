package task

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

// leasedTask is a task as the agent commands print it, with the lease of its claim.
type leasedTask struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Claim  *struct {
		By    string    `json:"by"`
		Until time.Time `json:"until"`
		Stale bool      `json:"stale"`
	} `json:"claim"`
}

// leasedDoc is the document of next, claim, comment, finish and release: {"profile", "task"}.
type leasedDoc struct {
	Task *leasedTask `json:"task"`
}

// agentRun runs an agent command of the default profile with --json, expects it to succeed
// and returns the task of its document.
func agentRun(t *testing.T, db string, args ...string) leasedTask {
	t.Helper()
	var doc leasedDoc
	mustTaskJSON(t, db, "default", &doc, "", args...)
	if doc.Task == nil {
		t.Fatalf("task %s: the document has no task", strings.Join(args, " "))
	}
	return *doc.Task
}

// agentLease runs an agent command that claims a task and returns when its lease ends.
func agentLease(t *testing.T, db string, args ...string) time.Time {
	t.Helper()
	task := agentRun(t, db, args...)
	if task.Claim == nil {
		t.Fatalf("task %s: the task is not held: %+v", strings.Join(args, " "), task)
	}
	return task.Claim.Until
}

// agentWithin checks that a lease that was asked for at some moment between before and after ends
// lease later, give or take the rounding of the store (a time is kept to the second, and the
// end is never earlier than asked for).
func agentWithin(t *testing.T, what string, got, before, after time.Time, lease time.Duration) {
	t.Helper()
	if got.Before(before.Add(lease-2*time.Second)) || got.After(after.Add(lease+2*time.Second)) {
		t.Errorf("%s: the lease ends at %s, want %s after a moment between %s and %s", what,
			got.Format(time.RFC3339), lease, before.Format(time.RFC3339), after.Format(time.RFC3339))
	}
}

// agentRefusal runs a command with --json and returns its exit code and the code and the
// message of the error document it printed ("" for none).
func agentRefusal(t *testing.T, db string, args ...string) (exit int, code, msg string) {
	t.Helper()
	out, _, err := runTask(t, db, "default", true, "", args...)
	var doc map[string]any
	if json.Unmarshal([]byte(out), &doc) != nil {
		doc = nil
	}
	code, _ = doc["code"].(string)
	msg, _ = doc["error"].(string)
	return exitCode(err), code, msg
}

// agentSnapshot is what a refused command must leave as it was: the revision of the boards and,
// for every task, its place, its holder, the time it was last touched and the size of its history.
func agentSnapshot(t *testing.T, dbPath string) string {
	t.Helper()
	raw, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var b strings.Builder
	revs, err := raw.DB.Query(`SELECT profile_id, rev FROM task_board_rev ORDER BY profile_id`)
	if err != nil {
		t.Fatal(err)
	}
	for revs.Next() {
		var profile string
		var rev int64
		if err := revs.Scan(&profile, &rev); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "revision of %s: %d\n", profile, rev)
	}
	if err := revs.Err(); err != nil {
		t.Fatal(err)
	}
	if err := revs.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err := raw.DB.Query(`SELECT id, profile_id, status, position, claimed_by, claim_until, updated_at,
		(SELECT COUNT(*) FROM task_events e WHERE e.task_id = tasks.id) FROM tasks ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, position int64
		var profile, status, holder, until, updated string
		var events int
		if err := rows.Scan(&id, &profile, &status, &position, &holder, &until, &updated, &events); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "#%d %s %s at %d, held by %q until %q, updated %s, %d events\n", id, profile, status, position, holder, until, updated, events)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// agentEvent is a row of a task's history.
type agentEvent struct{ Actor, Kind, From, To, Note string }

// agentEvents reads the history of a task straight from the table, oldest first, whatever the
// environment of the test says about who runs the commands.
func agentEvents(t *testing.T, dbPath string, taskID int64) []agentEvent {
	t.Helper()
	raw, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	rows, err := raw.DB.Query(`SELECT actor, kind, from_status, to_status, note FROM task_events WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []agentEvent
	for rows.Next() {
		var e agentEvent
		if err := rows.Scan(&e.Actor, &e.Kind, &e.From, &e.To, &e.Note); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

// agentKinds is the kinds of a history with the actor of each, oldest first: "you:created bot:claimed".
func agentKinds(events []agentEvent) string {
	words := make([]string, 0, len(events))
	for _, e := range events {
		words = append(words, e.Actor+":"+e.Kind)
	}
	return strings.Join(words, " ")
}

// agentSeedEvents gives a task n more events of history, written around the store in one
// transaction (n calls of seedTaskEvent would open the database n times).
func agentSeedEvents(t *testing.T, dbPath string, taskID int64, n int) {
	t.Helper()
	raw, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	tx, err := raw.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	at := time.Now().UTC().Format(storedStamp)
	for i := 0; i < n; i++ {
		if _, err := tx.Exec(`INSERT INTO task_events (task_id, at, actor, kind, from_status, to_status, note) VALUES (?, ?, 'seed', 'comment', '', '', ?)`,
			taskID, at, fmt.Sprintf("seeded note %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// agentVerbsFor is each way an agent runs a command that needs its name, on task n, written so
// that a name the store takes would get the command as far as the database.
func agentVerbsFor(n string) [][]string {
	return [][]string{
		{"next", "--claim"},
		{"claim", n},
		{"comment", n, "a note"},
		{"finish", n, "--result", "done"},
		{"release", n},
	}
}
