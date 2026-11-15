package people

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSON(t *testing.T, body string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// Re-importing a partial JSON row keeps the outreach draft, the review state
// and the other fields the row does not name.
func TestPeopleImportPartialRowKeepsDraftAndReviewState(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	full := writeJSON(t, `[{"username":"keep","full_name":"Kay","introduction":"Hi Kay","category":"approved","job_title":"CTO","website":"https://k.example","contact_details":"k@k.example","image_url":"https://i/k.png"}]`)
	if _, err := runPeople(t, cfg, "import", "--file", full, "--platform", "x"); err != nil {
		t.Fatal(err)
	}
	partial := writeJSON(t, `[{"username":"keep","follower_count":42}]`)
	if _, err := runPeople(t, cfg, "import", "--file", partial, "--platform", "x"); err != nil {
		t.Fatal(err)
	}
	var name, intro, cat, job, site, contact, img, followers sql.NullString
	if err := db.DB.QueryRow(`SELECT full_name, introduction, category, job_title, website, contact_details, image_url, follower_count
		FROM people WHERE platform='x' AND platform_username='keep'`).Scan(&name, &intro, &cat, &job, &site, &contact, &img, &followers); err != nil {
		t.Fatal(err)
	}
	got := []string{name.String, intro.String, cat.String, job.String, site.String, contact.String, img.String}
	want := []string{"Kay", "Hi Kay", "approved", "CTO", "https://k.example", "k@k.example", "https://i/k.png"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field %d = %q after a partial re-import, want %q", i, got[i], want[i])
		}
	}
	if followers.String != "42" {
		t.Errorf("follower_count = %q, want 42 (a JSON number)", followers.String)
	}
}

// Counts arrive as JSON numbers or strings; both are kept.
func TestPeopleImportCountsAcceptNumberAndString(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	f := writeJSON(t, `[{"username":"c1","follower_count":"1.2K","content_count":"1200","following_count":7}]`)
	if _, err := runPeople(t, cfg, "import", "--file", f, "--platform", "x"); err != nil {
		t.Fatal(err)
	}
	var followers string
	var content, following int
	if err := db.DB.QueryRow(`SELECT follower_count, content_count, following_count FROM people WHERE platform_username='c1'`).Scan(&followers, &content, &following); err != nil {
		t.Fatal(err)
	}
	if followers != "1.2K" || content != 1200 || following != 7 {
		t.Errorf("followers=%q content=%d following=%d, want 1.2K 1200 7", followers, content, following)
	}
}

// A non-numeric string count is an error naming the row, not a silent zero.
func TestPeopleImportBadCountNamesTheRow(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	f := writeJSON(t, `[{"username":"ok1"},{"username":"bad","content_count":"lots"}]`)
	_, err := runPeople(t, cfg, "import", "--file", f, "--platform", "x")
	if err == nil || !strings.Contains(err.Error(), "row 2") {
		t.Fatalf("err = %v, want an error naming row 2", err)
	}
	var n int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM people WHERE platform='x'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d people stored from a refused import, want 0", n)
	}
}

// A sent_at that is not RFC 3339 is reported with its row, and nothing is
// stored from that import.
func TestPeopleMessagesImportBadDateNamesTheRow(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	f := writeJSON(t, `[{"external_id":"m1","direction":"inbound","sent_at":"2026-07-20T10:00:00Z"},{"external_id":"m2","direction":"inbound","sent_at":"20/07/2026"}]`)
	_, err := runPeople(t, cfg, "messages", "import", "p1", "--file", f, "--source", "outlook")
	if err == nil || !strings.Contains(err.Error(), "row 2") || !strings.Contains(err.Error(), "sent_at") {
		t.Fatalf("err = %v, want an error naming row 2 and sent_at", err)
	}
	var n int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM person_messages WHERE person_id='p1'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d messages stored from a refused import, want 0", n)
	}
}

// Run by an agent, a partial row that leaves the draft out does not put an
// approved person back in the queue: the stored draft is unchanged.
func TestPeopleImportPartialRowByAgentKeepsApproval(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	if _, err := db.DB.Exec(`INSERT INTO people (id, profile_id, platform, platform_username, category, introduction)
		VALUES ('ap', 'default', 'x', 'appr', 'approved', 'the draft')`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDECODE", "1")
	f := writeJSON(t, `[{"username":"appr","follower_count":5}]`)
	if _, err := runPeople(t, cfg, "import", "--file", f, "--platform", "x"); err != nil {
		t.Fatal(err)
	}
	var cat, intro string
	if err := db.DB.QueryRow(`SELECT category, introduction FROM people WHERE id='ap'`).Scan(&cat, &intro); err != nil {
		t.Fatal(err)
	}
	if cat != "approved" || intro != "the draft" {
		t.Errorf("category=%q introduction=%q, want approved and the stored draft", cat, intro)
	}
}

// A count that is NaN, infinite, negative or absurdly large is refused with
// its row, whichever field carries it, as a string or as a number.
func TestPeopleImportRefusesNonsenseCounts(t *testing.T) {
	cases := []string{
		`{"username":"z","content_count":"NaN"}`,
		`{"username":"z","following_count":"inf"}`,
		`{"username":"z","content_count":"-Inf"}`,
		`{"username":"z","content_count":-5}`,
		`{"username":"z","following_count":"-3"}`,
		`{"username":"z","content_count":1e30}`,
		`{"username":"z","follower_count":"NaN"}`,
		`{"username":"z","follower_count":"+Inf"}`,
		`{"username":"z","follower_count":-1}`,
		`{"username":"z","follower_count":"-1"}`,
		`{"username":"z","follower_count":1e30}`,
		`{"username":"z","follower_count":"99999999999999999"}`,
	}
	for _, c := range cases {
		cfg, db := newReviewCLITestDB(t)
		f := writeJSON(t, `[{"username":"first"},`+c+`]`)
		_, err := runPeople(t, cfg, "import", "--file", f, "--platform", "x")
		if err == nil || !strings.Contains(err.Error(), "row 2") {
			t.Errorf("%s: err = %v, want an error naming row 2", c, err)
		}
		var n int
		_ = db.DB.QueryRow(`SELECT COUNT(*) FROM people WHERE platform='x'`).Scan(&n)
		if n != 0 {
			t.Errorf("%s: %d people stored from a refused import", c, n)
		}
	}
}
