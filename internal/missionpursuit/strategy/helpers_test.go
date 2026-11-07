package strategy

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/testdb"
)

var bg = context.Background()

type fixture struct {
	db      *sql.DB
	store   *Store
	session int64
	profile string // the decimal session id: the strategy layer's "profile"
}

// newFixture is a migrated database with one running mission task session.
func newFixture(t *testing.T) fixture {
	t.Helper()
	db := testdb.Open(t).DB
	f := fixture{db: db, store: NewStore(db)}
	f.session = addSession(t, db, "running")
	f.profile = strconv.FormatInt(f.session, 10)
	return f
}

func addSession(t *testing.T, db *sql.DB, state string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO task_sessions (profile_id, title, state, kind, created_by, created_at, updated_at) VALUES ('default', 't', ?, 'mission', 'you', 'x', 'x')`, state)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func (f fixture) setState(t *testing.T, state string) {
	t.Helper()
	mustExec(t, f.db, `UPDATE task_sessions SET state = ? WHERE id = ?`, state, f.session)
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func mustRefuse(t *testing.T, db *sql.DB, why, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err == nil {
		t.Errorf("%s: the database accepted %s", why, q)
	}
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f fixture) epoch(seq int64) types.Epoch {
	return types.Epoch{Profile: f.profile, Generation: 1, Seq: seq, Trigger: types.TriggerTimer, StartedAt: 1000 + seq, ModelPin: "fake/judge/1", State: types.EpochOpen}
}

func (f fixture) openEpoch(t *testing.T, seq int64) types.Epoch {
	t.Helper()
	e, created, err := f.store.InsertEpoch(bg, f.epoch(seq))
	if err != nil || !created {
		t.Fatalf("insert epoch %d: created=%t err=%v", seq, created, err)
	}
	return e
}

func (f fixture) direction(id, title string) types.Direction {
	return types.Direction{Profile: f.profile, Generation: 1, ID: id, KeyHash: types.KeyHash(strings.ToLower(title)), Title: title,
		State: types.DirProposed, ProposerSession: "planner-1", CreatedEpoch: 1}
}

func accepted(d types.Direction, seq int64) types.NoveltyRow {
	return types.NoveltyRow{EpochSeq: seq, Outcome: types.NoveltyAccepted}
}

func (f fixture) addDirection(t *testing.T, id, title string) types.Direction {
	t.Helper()
	d := f.direction(id, title)
	if err := f.store.AcceptProposal(bg, d, accepted(d, 1)); err != nil {
		t.Fatalf("accept %s: %v", id, err)
	}
	return d
}
