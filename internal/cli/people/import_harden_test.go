package people

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func seedImported(t *testing.T, cfg *testConfig, row string) {
	t.Helper()
	if _, err := runPeople(t, cfg, "import", "--file", writeJSON(t, "["+row+"]"), "--platform", "x"); err != nil {
		t.Fatal(err)
	}
}

func importRow(t *testing.T, db *storage.Database) (cat, intro string, verified, followers int) {
	t.Helper()
	err := db.DB.QueryRow(`SELECT COALESCE(category,''), COALESCE(introduction,''), is_verified, content_count FROM people WHERE platform_username='u'`).Scan(&cat, &intro, &verified, &followers)
	if err != nil {
		t.Fatal(err)
	}
	return
}

// The same approved category in another spelling keeps the stored one, for the
// user and for an agent alike.
func TestPeopleImportKeepsStoredApprovedSpelling(t *testing.T) {
	for _, agent := range []bool{false, true} {
		cfg, db := newReviewCLITestDB(t)
		asOperator(t)
		seedImported(t, cfg, `{"username":"u","introduction":"Hi","category":"approved"}`)
		if agent {
			t.Setenv("CLAUDECODE", "1")
		}
		seedImported(t, cfg, `{"username":"u","introduction":"Hi","category":"APPROVED "}`)
		if cat, _, _, _ := importRow(t, db); cat != "approved" {
			t.Errorf("agent=%v: stored category = %q, want the stored spelling %q", agent, cat, "approved")
		}
	}
}

// An agent's import never approves an unapproved person, however spelled.
func TestPeopleImportAgentCannotApprove(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	asOperator(t)
	seedImported(t, cfg, `{"username":"u","introduction":"Hi","category":"pending"}`)
	t.Setenv("CLAUDECODE", "1")
	_, err := runPeople(t, cfg, "import", "--file", writeJSON(t, `[{"username":"u","introduction":"Hi","category":" APPROVED"}]`), "--platform", "x")
	if err == nil {
		t.Fatal("an agent import approved a pending person")
	}
	if cat, _, _, _ := importRow(t, db); cat != "pending" {
		t.Errorf("category = %q after the refused import", cat)
	}
}

// An agent's import that clears the category or the text of an approved person
// does not leave them approved over changed text, and an empty category alone
// does not demote them.
func TestPeopleImportAgentClearsDoNotDemoteOrKeepApprovalOverNewText(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	asOperator(t)
	seedImported(t, cfg, `{"username":"u","introduction":"Hi","category":"approved"}`)
	t.Setenv("CLAUDECODE", "1")
	seedImported(t, cfg, `{"username":"u","category":""}`)
	if cat, intro, _, _ := importRow(t, db); cat != "approved" || intro != "Hi" {
		t.Fatalf("an agent's empty category changed the person: %q %q", cat, intro)
	}
	seedImported(t, cfg, `{"username":"u","introduction":"Changed"}`)
	if cat, intro, _, _ := importRow(t, db); cat == "approved" || intro != "Changed" {
		t.Fatalf("approval survived changed text: %q %q", cat, intro)
	}
}

// A row that names a field clears it; one that leaves it out keeps it.
func TestPeopleImportNamedFieldClears(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	asOperator(t)
	seedImported(t, cfg, `{"username":"u","introduction":"Hi","category":"pending","is_verified":true,"content_count":5}`)
	seedImported(t, cfg, `{"username":"u","job_title":"x"}`)
	if cat, intro, v, c := importRow(t, db); cat != "pending" || intro != "Hi" || v != 1 || c != 5 {
		t.Fatalf("a partial row changed fields: %q %q %d %d", cat, intro, v, c)
	}
	seedImported(t, cfg, `{"username":"u","introduction":"","category":null,"is_verified":false,"content_count":0}`)
	if cat, intro, v, c := importRow(t, db); cat != "" || intro != "" || v != 0 || c != 0 {
		t.Fatalf("named fields were not cleared: %q %q %d %d", cat, intro, v, c)
	}
}

// A bad row N leaves nothing of rows before it, and an oversize file is refused.
func TestPeopleMessagesImportAtomicAndCapped(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	f := writeJSON(t, `[{"external_id":"a","body":"one"},{"external_id":"b","sent_at":"nope"}]`)
	if _, err := runPeople(t, cfg, "messages", "import", "p1", "--file", f, "--source", "gmail"); err == nil {
		t.Fatal("bad date accepted")
	}
	var n int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM person_messages`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows stored from a refused file", n)
	}
	// A failure in the store (unknown person) names that nothing was stored.
	f = writeJSON(t, `[{"external_id":"a","body":"one"}]`)
	_, err := runPeople(t, cfg, "messages", "import", "nobody", "--file", f, "--source", "gmail")
	if err == nil {
		t.Fatal("unknown person accepted")
	}

	big := filepath.Join(t.TempDir(), "big.json")
	fh, _ := os.Create(big)
	_ = fh.Truncate(maxImportFileBytes + 1)
	fh.Close()
	for _, args := range [][]string{
		{"messages", "import", "p1", "--file", big, "--source", "gmail"},
		{"import", "--file", big, "--platform", "x"},
	} {
		if _, err := runPeople(t, cfg, args...); err == nil || !strings.Contains(err.Error(), "import limit") {
			t.Errorf("%v: %v", args, err)
		}
	}
}
