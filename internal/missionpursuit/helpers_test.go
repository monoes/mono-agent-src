package missionpursuit

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

var (
	bg    = context.Background()
	human = tasks.Actor{Kind: tasks.Human}
)

func agent(name string) tasks.Actor { return tasks.Actor{Kind: tasks.Agent, Name: name} }

// goodCriteria is a valid set: one machine criterion and the rubric the person decides.
func goodCriteria() []Criterion {
	return []Criterion{
		{Kind: "command", Spec: "go test ./...", Expected: "exit:0"},
		{Kind: "rubric", Spec: "The report reads well to the person who asked for it"},
	}
}

func otherCriteria() []Criterion {
	return []Criterion{
		{Kind: "file", Spec: "docs/report.md", Expected: "exists"},
		{Kind: "rubric", Spec: "Nothing in the report is made up"},
	}
}

// fixture is a migrated database with a mission chat: a coder_sessions row in mode, and a running task session of the
// given kind linked to it.
type fixture struct {
	db      *sql.DB
	store   *Store
	session int64
}

func newFixture(t *testing.T, mode, kind string) fixture {
	t.Helper()
	db := testdb.Open(t).DB
	f := fixture{db: db, store: NewStore(db)}
	if mode != "" {
		mustExec(t, db, `INSERT INTO coder_sessions (profile_id, conversation_id, chat_mode, created_at, updated_at) VALUES ('default', 'chat1', ?, 'x', 'x')`, mode)
	}
	f.session = addSession(t, db, "default", kind, "chat1", "running")
	return f
}

func addSession(t *testing.T, db *sql.DB, profile, kind, conv, state string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO task_sessions (profile_id, conversation_id, title, state, kind, created_by, created_at, updated_at) VALUES (?, ?, 't', ?, ?, 'you', 'x', 'x')`, profile, conv, state, kind)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func newMission(t *testing.T) fixture { return newFixture(t, "mission", tasks.SessionKindMission) }

func mustPropose(t *testing.T, f fixture, cs []Criterion, by tasks.Actor) Goal {
	t.Helper()
	g, err := f.store.Propose(bg, "default", f.session, cs, by)
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	return g
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantIs(t *testing.T, err, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: err = %v, want %v", what, err, target)
	}
}

// failModes is a ModeReader that cannot read.
type failModes struct{}

func (failModes) ChatMode(context.Context, tasks.Queryer, string, string) (string, error) {
	return "", errors.New("disk on fire")
}

// fixedModes is a ModeReader that answers one mode whatever the chat.
type fixedModes string

func (m fixedModes) ChatMode(context.Context, tasks.Queryer, string, string) (string, error) {
	return string(m), nil
}
