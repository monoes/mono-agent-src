package peoplereview

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func addPerson(t *testing.T, db *sql.DB, id, profile, category, intro string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO people (id, profile_id, platform, platform_username, full_name, category, introduction)
		VALUES (?, ?, 'LINKEDIN', ?, ?, ?, ?)`, id, profile, id+"-user", "Name "+id, category, intro); err != nil {
		t.Fatal(err)
	}
}

func TestQueue(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	addPerson(t, db, "p1", "prof", Pending, "hi")
	addPerson(t, db, "p2", "prof", Pending, "hello")
	addPerson(t, db, "p3", "prof", "lead", "")
	addPerson(t, db, "p4", "other", Pending, "")

	got, err := ListPending(ctx, db, "prof")
	if err != nil || len(got) != 2 {
		t.Fatalf("ListPending = %v, %v; want p1 and p2 only", got, err)
	}

	p, err := Approve(ctx, db, "prof", "p1", "edited intro")
	if err != nil || p.Category != Approved || p.Introduction != "edited intro" {
		t.Fatalf("Approve = %+v, %v", p, err)
	}
	// An empty intro keeps the drafted one.
	if p, _ := Approve(ctx, db, "prof", "p2", "  "); p.Introduction != "hello" {
		t.Fatalf("empty intro replaced the draft: %+v", p)
	}
	// A second approval (double send) and approving a non-pending person fail.
	if _, err := Approve(ctx, db, "prof", "p1", ""); !errors.Is(err, ErrNotPending) {
		t.Fatalf("re-approve: %v", err)
	}
	if _, err := Approve(ctx, db, "prof", "p3", ""); !errors.Is(err, ErrNotPending) {
		t.Fatalf("approve non-pending: %v", err)
	}
	if err := Reject(ctx, db, "prof", "p3"); err != nil {
		t.Fatal(err)
	}
	if got, _ := ListPending(ctx, db, "prof"); len(got) != 0 {
		t.Fatalf("queue not empty: %v", got)
	}

	// Another profile's person is out of reach.
	if _, err := Approve(ctx, db, "prof", "p4", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approve other profile's person: %v", err)
	}
	if err := Reject(ctx, db, "prof", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reject unknown: %v", err)
	}
}

func TestPickSendWorkflow(t *testing.T) {
	// None: an error that says how to choose.
	if _, err := PickSendWorkflow([]Workflow{{ID: "x", Name: "Scrape"}}, ""); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), SendWorkflowName) {
		t.Fatalf("no workflow: %v", err)
	}

	wfs := []Workflow{{ID: "w0", Name: "Scrape"}, {ID: "w1", Name: "LinkedIn: send approved DMs"}}
	if w, err := PickSendWorkflow(wfs, ""); err != nil || w.ID != "w1" {
		t.Fatalf("convention: %+v, %v", w, err)
	}

	wfs = append(wfs, Workflow{ID: "w2", Name: "Send Approved DMs (X)"})
	if _, err := PickSendWorkflow(wfs, ""); !errors.Is(err, ErrAmbiguous) || !strings.Contains(err.Error(), "2 workflows in this profile") {
		t.Fatalf("ambiguous: %v", err)
	}
	// Named explicitly, by id or by name.
	if w, err := PickSendWorkflow(wfs, "w2"); err != nil || w.ID != "w2" {
		t.Fatalf("by id: %+v, %v", w, err)
	}
	if w, err := PickSendWorkflow(wfs, "(x)"); err != nil || w.ID != "w2" {
		t.Fatalf("by name: %+v, %v", w, err)
	}
	if _, err := PickSendWorkflow(wfs, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
}

// The guard's lookup finds a person whatever spelling of the platform is
// stored or asked for.
func TestStoredPersonIsCaseInsensitiveOnPlatform(t *testing.T) {
	db := testDB(t)
	addPerson(t, db, "p1", "prof", Approved, "hi") // stored LINKEDIN
	for _, plat := range []string{"LINKEDIN", "linkedin", "LinkedIn"} {
		s, err := StoredPerson(context.Background(), db, "prof", plat, "p1-user")
		if err != nil || !s.Found || s.Category != Approved {
			t.Errorf("StoredPerson(%q) = %+v, %v", plat, s, err)
		}
	}
	if s, _ := StoredPerson(context.Background(), db, "prof", "linkedin", "P1-USER"); s.Found {
		t.Error("the username is compared exactly, as the unique key does")
	}
}

// With twin spellings stored, the guard reads the same row the write updates.
func TestStoredPersonAgreesWithResolvePlatformOnTwins(t *testing.T) {
	db := testDB(t)
	for _, r := range [][]string{{"a", "linkedin", Approved, "2026-01-01 00:00:00"}, {"b", "LINKEDIN", Pending, "2026-02-01 00:00:00"}} {
		if _, err := db.Exec(`INSERT INTO people (id, profile_id, platform, platform_username, category, introduction, updated_at)
			VALUES (?, 'prof', ?, 'ada', ?, 'hi', ?)`, r[0], r[1], r[2], r[3]); err != nil {
			t.Fatal(err)
		}
	}
	for _, ask := range []string{"linkedin", "LINKEDIN", "LinkedIn"} {
		spelling, err := storage.ResolvePlatform(db, "prof", ask, "ada")
		if err != nil {
			t.Fatal(err)
		}
		viaWrite, _ := StoredPerson(context.Background(), db, "prof", spelling, "ada")
		viaGuard, _ := StoredPerson(context.Background(), db, "prof", ask, "ada")
		if viaWrite.Category != viaGuard.Category {
			t.Errorf("ask %q: the write updates the %q row (%s), the guard read %s", ask, spelling, viaWrite.Category, viaGuard.Category)
		}
	}
}

// The same username in another profile is not the person.
func TestStoredPersonIsPerProfile(t *testing.T) {
	db := testDB(t)
	addPerson(t, db, "p1", "prof", Approved, "hi")
	if s, _ := StoredPerson(context.Background(), db, "other", "linkedin", "p1-user"); s.Found {
		t.Fatal("found another profile's person")
	}
}
