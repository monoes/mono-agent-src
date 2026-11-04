package people

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/peoplereview"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// asOperator makes this test process the user's own terminal.
func asOperator(t *testing.T) {
	t.Helper()
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
}

// notOperatorMarkers each make a process not the user's: an agent's shell,
// the chat's run and a workflow run.
var notOperatorMarkers = []string{"CLAUDECODE", browserctl.EnvAICaller, workflow.EnvWorkflowRun}

func personRow(t *testing.T, db *storage.Database, where string, args ...any) (category, intro string, found bool) {
	t.Helper()
	err := db.DB.QueryRow(`SELECT COALESCE(category,''), COALESCE(introduction,'') FROM people WHERE `+where, args...).Scan(&category, &intro)
	if err != nil {
		return "", "", false
	}
	return category, intro, true
}

// Approving lets the drafted DM go out, so only the user approves, in every
// form; queueing stays open, rejecting is the user's too (#831 S5).
func TestPeopleReviewApproveIsTheUsers(t *testing.T) {
	for _, marker := range notOperatorMarkers {
		for _, args := range [][]string{
			{"approve", "p1"},
			{"approve", "p1", "--intro", "Hi from an agent"},
			{"approve", "p1", "--send"},
			{"approve", "p1", "--send-plan"},
		} {
			t.Run(marker+" "+strings.Join(args[2:], " "), func(t *testing.T) {
				cfg, db := newReviewCLITestDB(t)
				t.Setenv(marker, "1")
				_, err := runReview(t, cfg, args...)
				if exitCode(err) != 3 || !strings.Contains(err.Error(), "only the user approves") || !strings.Contains(err.Error(), marker) {
					t.Fatalf("approve as %s: exit %d: %v", marker, exitCode(err), err)
				}
				if cat, intro, _ := personRow(t, db, "id = ?", "p1"); cat != peoplereview.Pending || intro != "drafted" {
					t.Fatalf("approve as %s changed p1: %q %q", marker, cat, intro)
				}
				if _, err := runReview(t, cfg, "reject", "p2"); exitCode(err) != 3 || !strings.Contains(err.Error(), "only the user rejects") || category(t, db, "p2") == peoplereview.Rejected {
					t.Fatalf("reject as %s: %v", marker, err)
				}
			})
		}
	}

	cfg, db := newReviewCLITestDB(t)
	if _, err := runReview(t, cfg, "approve", "p1", "--intro", "Hi Sam"); err != nil {
		t.Fatalf("the user approves: %v", err)
	}
	if cat, intro, _ := personRow(t, db, "id = ?", "p1"); cat != peoplereview.Approved || intro != "Hi Sam" {
		t.Fatalf("the user's approval: %q %q", cat, intro)
	}
}

func runImport(t *testing.T, cfg *testConfig, people string) error {
	t.Helper()
	file := filepath.Join(t.TempDir(), "people.json")
	if err := os.WriteFile(file, []byte(people), 0o600); err != nil {
		t.Fatal(err)
	}
	var err error
	captureStdout(t, func() {
		cmd := testDeps(cfg).newPeopleImportCmd()
		cmd.SetArgs([]string{"--file", file, "--platform", "linkedin"})
		err = cmd.Execute()
	})
	return err
}

// An import someone else runs may queue people but never approves anyone,
// and a new text for an approved person goes back to the queue.
func TestPeopleImportNeverApprovesForAnAgent(t *testing.T) {
	for _, marker := range notOperatorMarkers {
		t.Run(marker, func(t *testing.T) {
			cfg, db := newReviewCLITestDB(t)
			if _, err := db.DB.Exec(`INSERT INTO people (id, profile_id, platform, platform_username, category, introduction)
				VALUES ('a1', 'default', 'linkedin', 'ada', 'approved', 'Hi Ada')`); err != nil {
				t.Fatal(err)
			}
			t.Setenv(marker, "1")

			for _, people := range []string{
				`[{"username":"queued","category":"pending_approval"},{"username":"bob","category":"approved","introduction":"Hi Bob"}]`,
				`[{"username":"ada","category":"Approved","introduction":"Hi Ada, again"}]`,
			} {
				if err := runImport(t, cfg, people); exitCode(err) != 3 || !strings.Contains(err.Error(), "only the user approves") {
					t.Fatalf("import %s as %s: exit %d: %v", people, marker, exitCode(err), err)
				}
			}
			if _, _, found := personRow(t, db, "platform_username IN ('bob', 'queued')"); found {
				t.Fatal("a refused import saved someone")
			}
			if cat, intro, _ := personRow(t, db, "id = 'a1'"); cat != peoplereview.Approved || intro != "Hi Ada" {
				t.Fatalf("a refused import changed ada: %q %q", cat, intro)
			}

			if err := runImport(t, cfg, `[{"username":"bob","category":"pending_approval","introduction":"Hi Bob"},{"username":"ada","introduction":"Hi Ada, again"}]`); err != nil {
				t.Fatalf("queueing as %s: %v", marker, err)
			}
			if cat, _, _ := personRow(t, db, "platform_username = 'bob'"); cat != peoplereview.Pending {
				t.Fatalf("bob = %q, want queued", cat)
			}
			if cat, intro, _ := personRow(t, db, "id = 'a1'"); cat != peoplereview.Pending || intro != "Hi Ada, again" {
				t.Fatalf("ada's new text: %q %q, want back in the queue", cat, intro)
			}
		})
	}

	cfg, db := newReviewCLITestDB(t)
	if err := runImport(t, cfg, `[{"username":"cy","category":"approved","introduction":"Hi Cy"}]`); err != nil {
		t.Fatalf("the user's import: %v", err)
	}
	if cat, _, _ := personRow(t, db, "platform_username = 'cy'"); cat != peoplereview.Approved {
		t.Fatalf("the user's import: cy = %q", cat)
	}
}
