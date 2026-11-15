package people

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/peoplereview"
)

func runImportAs(t *testing.T, cfg *testConfig, platform, people string) error {
	t.Helper()
	file := filepath.Join(t.TempDir(), "people.json")
	if err := os.WriteFile(file, []byte(people), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() {
		cmd := testDeps(cfg).newPeopleImportCmd()
		cmd.SetArgs([]string{"--file", file, "--platform", platform})
		err = cmd.Execute()
	})
	return err
}

// An import under another spelling of the platform updates the stored row
// (keeping its spelling) instead of adding a twin.
func TestPeopleImportUpdatesTheRowOfAnotherPlatformSpelling(t *testing.T) {
	cfg, db := newReviewCLITestDB(t) // p1-user is stored as LINKEDIN
	if err := runImportAs(t, cfg, "linkedin", `[{"username":"p1-user","full_name":"Renamed"}]`); err != nil {
		t.Fatal(err)
	}
	var n int
	var name, plat string
	db.DB.QueryRow(`SELECT COUNT(*), MAX(full_name), MAX(platform) FROM people WHERE platform_username = 'p1-user'`).Scan(&n, &name, &plat)
	if n != 1 || name != "Renamed" || plat != "LINKEDIN" {
		t.Fatalf("rows=%d name=%q platform=%q, want 1 Renamed LINKEDIN", n, name, plat)
	}
	// A new person gets the one canonical spelling, whatever the flag says.
	if err := runImportAs(t, cfg, "linkedin", `[{"username":"fresh"}]`); err != nil {
		t.Fatal(err)
	}
	db.DB.QueryRow(`SELECT platform FROM people WHERE platform_username = 'fresh'`).Scan(&plat)
	if plat != "LINKEDIN" {
		t.Fatalf("new row platform = %q, want LINKEDIN", plat)
	}
}

// The review guard finds an approved row whatever spelling the import uses.
func TestPeopleImportGuardFindsApprovedRowInAnySpelling(t *testing.T) {
	for _, stored := range []string{"LINKEDIN", "linkedin", "LinkedIn"} {
		t.Run(stored, func(t *testing.T) {
			cfg, db := newReviewCLITestDB(t)
			if _, err := db.DB.Exec(`INSERT INTO people (id, profile_id, platform, platform_username, category, introduction)
				VALUES ('a1', 'default', ?, 'ada', 'approved', 'Hi Ada')`, stored); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CLAUDECODE", "1")
			err := runImportAs(t, cfg, "linkedin", `[{"username":"ada","introduction":"Hi Ada, changed"}]`)
			if err != nil {
				t.Fatal(err)
			}
			cat, intro, _ := personRow(t, db, "id = 'a1'")
			if cat != peoplereview.Pending || intro != "Hi Ada, changed" {
				t.Fatalf("changed text for an approved person: %q %q, want back in the queue", cat, intro)
			}
			err = runImportAs(t, cfg, "linkedin", `[{"username":"ada","category":"approved","introduction":"Hi Ada, changed"}]`)
			if err == nil || !strings.Contains(err.Error(), "only the user approves") {
				t.Fatalf("an agent approving: %v", err)
			}
		})
	}
}

// Re-running `messages import` keeps metadata (a classification) the file does
// not carry, and the status the operator set.
func TestMessagesImportRerunKeepsMetadataAndStatus(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	file := filepath.Join(t.TempDir(), "m.json")
	if err := os.WriteFile(file, []byte(`[{"external_id":"e1","direction":"inbound","subject":"s","body":"b1"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runPeople(t, cfg, "messages", "import", "p1", "--file", file, "--source", "outlook"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE person_messages SET status = 'approved', metadata = '{"_classification":{"intent":"interested"}}' WHERE external_id = 'e1'`); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`[{"external_id":"e1","direction":"inbound","subject":"s","body":"b2"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runPeople(t, cfg, "messages", "import", "p1", "--file", file, "--source", "outlook"); err != nil {
		t.Fatal(err)
	}
	var status, md, body string
	db.DB.QueryRow(`SELECT status, COALESCE(metadata,''), body FROM person_messages WHERE external_id = 'e1'`).Scan(&status, &md, &body)
	if body != "b2" || status != "approved" || !strings.Contains(md, "interested") {
		t.Fatalf("after re-import: body=%q status=%q metadata=%q", body, status, md)
	}
}
