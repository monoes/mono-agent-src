package people

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type dedupeOut struct {
	Applied bool   `json:"applied"`
	Backup  string `json:"backup"`
	Groups  []struct {
		Winner string `json:"winner"`
		Reason string `json:"reason"`
	} `json:"groups"`
	Results []struct {
		Merged int    `json:"merged"`
		Err    string `json:"error"`
	} `json:"results"`
}

func TestPeopleDedupeIsADryRunByDefault(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	for _, q := range []string{
		`INSERT INTO people (id, profile_id, platform, platform_username, full_name) VALUES ('d1', 'default', 'instagram', 'sam', '')`,
		`INSERT INTO people (id, profile_id, platform, platform_username, full_name) VALUES ('d2', 'default', 'INSTAGRAM', 'sam', 'Sam Lee')`,
	} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runPeople(t, cfg, "dedupe")
	if err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	var got dedupeOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if got.Applied || len(got.Groups) != 1 || got.Groups[0].Winner != "d2" || got.Backup != "" {
		t.Fatalf("dry run = %+v", got)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM people WHERE id IN ('d1','d2')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("a dry run changed people: %d %v", n, err)
	}
}

func TestPeopleDedupeApply(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	for _, q := range []string{
		`INSERT INTO people (id, profile_id, platform, platform_username, full_name) VALUES ('d1', 'default', 'instagram', 'sam', '')`,
		`INSERT INTO people (id, profile_id, platform, platform_username, full_name) VALUES ('d2', 'default', 'INSTAGRAM', 'sam', 'Sam Lee')`,
	} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	undo := filepath.Join(t.TempDir(), "undo.json")
	out, err := runPeople(t, cfg, "dedupe", "--apply", "--backup", undo)
	if err != nil {
		t.Fatalf("dedupe --apply: %v", err)
	}
	var got dedupeOut
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if !got.Applied || got.Backup != undo || len(got.Results) != 1 || got.Results[0].Merged != 1 {
		t.Fatalf("apply = %+v", got)
	}
	if _, err := os.Stat(undo); err != nil {
		t.Fatalf("undo file: %v", err)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM people WHERE id = 'd1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("loser still there: %d %v", n, err)
	}
	// Run again: nothing left to merge, and no undo file is written.
	out, err = runPeople(t, cfg, "dedupe", "--apply", "--backup", filepath.Join(t.TempDir(), "again.json"))
	if err != nil || !strings.Contains(out, `"groups": []`) {
		t.Fatalf("second run = %q, %v", out, err)
	}
}

// Merging removes rows, so only the user applies; the listing stays open.
func TestPeopleDedupeApplyIsTheUsers(t *testing.T) {
	for _, marker := range notOperatorMarkers {
		t.Run(marker, func(t *testing.T) {
			cfg, db := newReviewCLITestDB(t)
			for _, q := range []string{
				`INSERT INTO people (id, profile_id, platform, platform_username) VALUES ('d1', 'default', 'instagram', 'sam')`,
				`INSERT INTO people (id, profile_id, platform, platform_username) VALUES ('d2', 'default', 'INSTAGRAM', 'sam')`,
			} {
				if _, err := db.DB.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv(marker, "1")
			undo := filepath.Join(t.TempDir(), "undo.json")
			if _, err := runPeople(t, cfg, "dedupe", "--apply", "--backup", undo); exitCode(err) != 3 || !strings.Contains(err.Error(), "only the user") {
				t.Fatalf("apply as %s: exit %d: %v", marker, exitCode(err), err)
			}
			if _, err := os.Stat(undo); err == nil {
				t.Fatal("a refused apply wrote an undo file")
			}
			var n int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM people WHERE id IN ('d1','d2')`).Scan(&n); err != nil || n != 2 {
				t.Fatalf("a refused apply changed people: %d %v", n, err)
			}
			if _, err := runPeople(t, cfg, "dedupe"); err != nil {
				t.Fatalf("the dry run is open to %s: %v", marker, err)
			}
		})
	}
}
