package peopledupes

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

const rawSecret = "vsecret-777"

// The undo file must never be a second copy of message text, and what it does
// keep of a message must have gone through the scrub first (#1594): a card's
// old value is removed from person_messages by the read hook, and a raw SELECT
// of the table would write it back out in plaintext.
func TestUndoFileHoldsNoMessageTextAndOnlyScrubbedDroppedRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	person(t, db, "w", "prof", "INSTAGRAM", "sam", "Sam Lee", "")
	person(t, db, "l", "prof", "instagram", "sam", "", "")
	// m1 moves to the winner; m2 is a duplicate of the winner's own m3 and
	// is dropped with the loser, so it is the only copy.
	exec(t, db, `INSERT INTO person_messages (id, person_id, source, external_id, sender, subject, body, metadata, profile_id) VALUES
		('m1', 'l', 'x', 'e1', 'sender-moved', 'subject-moved', 'body-moved '||?, '{"k":"meta-moved"}', 'prof'),
		('m2', 'l', 'x', 'e2', 'sender-dropped', 'subject-dropped', 'body-dropped '||?, '{"k":"meta-dropped"}', 'prof'),
		('m3', 'w', 'x', 'e2', 'sender-own', 'subject-own', 'body-own', '{}', 'prof')`, rawSecret, rawSecret)

	// Stands in for hilscrub.Ensure (storage cannot import it): it removes
	// the secret from every message of the profile.
	calls := 0
	old := storage.ReadHook
	t.Cleanup(func() { storage.ReadHook = old })
	storage.ReadHook = func(_ context.Context, d *sql.DB, profile string) {
		calls++
		if _, err := d.Exec(`UPDATE person_messages SET body = REPLACE(body, ?, '[[held-secret]]') WHERE profile_id = ?`, rawSecret, profile); err != nil {
			t.Error(err)
		}
	}

	groups, err := Find(ctx, db, "prof")
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	backup := undoFile(t)
	if _, err := Apply(ctx, db, "prof", groups, backup); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Error("Apply did not scrub the messages before it saved any of them")
	}
	raw, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	if strings.Contains(s, rawSecret) {
		t.Errorf("the undo file holds a scrubbed secret:\n%s", s)
	}
	for _, leaked := range []string{"body-moved", "subject-moved", "sender-moved", "meta-moved", "body-own"} {
		if strings.Contains(s, leaked) {
			t.Errorf("the undo file holds message text of a row that was only re-pointed (%q):\n%s", leaked, s)
		}
	}
	// the dropped duplicate is gone from the database: its undo copy is the
	// only one, so it keeps its content (scrubbed)
	for _, want := range []string{"body-dropped", "[[held-secret]]", `"m2"`} {
		if !strings.Contains(s, want) {
			t.Errorf("the undo file lost the dropped message (%q):\n%s", want, s)
		}
	}
	// the re-pointed one is still undoable by id
	if !strings.Contains(s, `"m1"`) {
		t.Errorf("the re-pointed message is not in the undo file:\n%s", s)
	}
}

// UNIQUE(person_id, source, external_id) with an empty external_id: a note of
// the loser that looks like a note the winner has is dropped with the loser
// (UPDATE OR IGNORE, then the cascade). The dry run must count those apart
// from the rows that really move.
func TestDryRunCountsRowsThatWouldBeDroppedSeparately(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	person(t, db, "w", "prof", "INSTAGRAM", "sam", "Sam Lee", "")
	person(t, db, "l", "prof", "instagram", "sam", "", "")
	exec(t, db, `INSERT INTO person_messages (id, person_id, source, external_id, body, profile_id) VALUES
		('n-own', 'w', 'manual', '', 'winner note', 'prof'),
		('n-lost', 'l', 'manual', '', 'a different note of the loser', 'prof'),
		('m-move', 'l', 'x', 'e1', 'moves', 'prof')`)
	groups, err := Find(ctx, db, "prof")
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	g := groups[0]
	if got := g.Repoint["person_messages.person_id"]; got != 1 {
		t.Errorf("would move %d messages, want 1 (the other collides with the winner's note)", got)
	}
	if got := g.Dropped["person_messages.person_id"]; got != 1 {
		t.Errorf("would be dropped as duplicates: %d, want 1 (%+v)", got, g.Dropped)
	}
	// the dry run changed nothing
	if n := count(t, db, `SELECT COUNT(*) FROM person_messages WHERE person_id = 'l'`); n != 2 {
		t.Errorf("Find moved rows: the loser has %d, want 2", n)
	}
	// and what it said is what Apply does
	res, err := Apply(ctx, db, "prof", groups, undoFile(t))
	if err != nil || res[0].Err != "" {
		t.Fatalf("apply: %+v, %v", res, err)
	}
	if res[0].Moved["person_messages.person_id"] != 1 {
		t.Errorf("moved = %+v", res[0].Moved)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM person_messages WHERE person_id = 'w'`); n != 2 {
		t.Errorf("w has %d messages, want its own note + the one that moved", n)
	}
}
