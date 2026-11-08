package inputs

import (
	"context"
	"database/sql"
	"strconv"
	"testing"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

var bg = context.Background()

type fixture struct {
	db      *sql.DB
	reader  *Reader
	session int64
}

// newFixture is a migrated database with one running mission task session, no pursuit.
func newFixture(t *testing.T) fixture {
	t.Helper()
	db := testdb.Open(t).DB
	f := fixture{db: db, reader: New(db)}
	mustExec(t, db, `INSERT INTO coder_sessions (profile_id, conversation_id, chat_mode, created_at, updated_at) VALUES ('default', 'chat1', 'mission', 'x', 'x')`)
	res, err := db.Exec(`INSERT INTO task_sessions (profile_id, conversation_id, title, state, kind, created_by, created_at, updated_at) VALUES ('default', 'chat1', 't', 'running', 'mission', 'you', 'x', 'x')`)
	if err != nil {
		t.Fatal(err)
	}
	f.session, _ = res.LastInsertId()
	return f
}

func addSession(t *testing.T, db *sql.DB, state, kind string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO task_sessions (profile_id, title, state, kind, created_by, created_at, updated_at) VALUES ('default', 't', ?, ?, 'you', 'x', 'x')`, state, kind)
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

// startPursuit starts the pursuit through the policy store (the one writer), so the rows are the ones the host will see.
func (f fixture) startPursuit(t *testing.T) {
	t.Helper()
	ps := missionpursuit.NewStore(f.db)
	g, err := ps.Propose(bg, "default", f.session, []missionpursuit.Criterion{
		{Kind: "command", Spec: "go test ./...", Expected: "exit:0"},
		{Kind: "rubric", Spec: "The report reads well"},
	}, tasks.Actor{Kind: tasks.Agent, Name: "lead", Conversation: "chat1"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	human := tasks.Actor{Kind: tasks.Human}
	if _, err := ps.Confirm(bg, "default", f.session, g.Hash, human); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := ps.Start(bg, "default", f.session, missionpursuit.AwayLimits{}, human); err != nil {
		t.Fatalf("start: %v", err)
	}
}

func (f fixture) pause(t *testing.T) {
	t.Helper()
	mustExec(t, f.db, `UPDATE mission_pursuit SET paused_by='you' WHERE session_id=?`, f.session)
}

func (f fixture) setState(t *testing.T, state string) {
	t.Helper()
	mustExec(t, f.db, `UPDATE task_sessions SET state=? WHERE id=?`, state, f.session)
}

func (f fixture) addTask(t *testing.T, status string, blocked int) int64 {
	t.Helper()
	res, err := f.db.Exec(`INSERT INTO tasks (profile_id, title, status, position, session_id, blocked, created_at, updated_at) VALUES ('default', 't', ?, 1, ?, ?, 'x', 'x')`, status, f.session, blocked)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
