package nodes

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/dbconn/dbconntest"
	"github.com/monoes/mono-agent/internal/storage"
)

func newTargetsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "targets.db"))
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	return db.DB
}

// `node run` executes a browser node standalone under ExecutionID "cli",
// which has no workflow_executions row: saving extracted items must keep
// the targets unattached instead of failing the action on the FK.
func TestSaveExtractedDataWithoutWorkflowExecution(t *testing.T) {
	db := newTargetsDB(t)

	s := &workflowActionStorage{db: db, profileID: "p1", executionID: "cli", nodeID: "cli-node", platform: "hackernews"}
	items := []map[string]interface{}{{"url": "https://news.ycombinator.com/item?id=1"}}
	if err := s.SaveExtractedData("a", items); err != nil {
		t.Fatalf("SaveExtractedData: %v", err)
	}

	var execID sql.NullString
	if err := db.QueryRow(`SELECT execution_id FROM workflow_node_targets WHERE node_id = 'cli-node'`).Scan(&execID); err != nil {
		t.Fatalf("reading target: %v", err)
	}
	if execID.Valid {
		t.Errorf("execution_id = %q, want NULL for a standalone run", execID.String)
	}
}

func TestSaveExtractedDataAttachesToWorkflowExecution(t *testing.T) {
	db := newTargetsDB(t)

	if _, err := db.Exec(`INSERT INTO workflows (id, name) VALUES ('w1', 'w')`); err != nil {
		t.Fatalf("seeding workflow: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workflow_executions (id, workflow_id) VALUES ('e1', 'w1')`); err != nil {
		t.Fatalf("seeding execution: %v", err)
	}

	s := &workflowActionStorage{db: db, profileID: "p1", executionID: "e1", nodeID: "n1", platform: "hackernews"}
	if err := s.SaveExtractedData("a", []map[string]interface{}{{"url": "https://news.ycombinator.com/item?id=1"}}); err != nil {
		t.Fatalf("SaveExtractedData: %v", err)
	}

	var execID sql.NullString
	if err := db.QueryRow(`SELECT execution_id FROM workflow_node_targets WHERE node_id = 'n1'`).Scan(&execID); err != nil {
		t.Fatalf("reading target: %v", err)
	}
	if execID.String != "e1" {
		t.Errorf("execution_id = %v, want e1", execID)
	}
}

// Saving extracted LinkedIn posts made each post a "person" named after
// its permalink's last segment (urn:li:activity:…). A post is not a person:
// its author is, and a post without one links to nobody.
func TestSaveExtractedDataLinksPostAuthorsNotPosts(t *testing.T) {
	db := newTargetsDB(t)
	s := &workflowActionStorage{db: db, profileID: "p1", executionID: "cli", nodeID: "cli-node", platform: "linkedin"}
	items := []map[string]interface{}{
		{"url": "https://www.linkedin.com/feed/update/urn:li:activity:7509522219027554305/", "urn": "urn:li:activity:7509522219027554305",
			"author": "Ada First", "author_url": "https://www.linkedin.com/in/ada-first-test/", "text_preview": "Hello"},
		{"url": "https://www.linkedin.com/feed/update/urn:li:activity:7508827577256460288/",
			"author": "Example Works", "author_url": "https://www.linkedin.com/company/example-works/"},
		{"url": "https://www.linkedin.com/feed/update/urn:li:activity:7509235263131398145/"},
	}
	if err := s.SaveExtractedData("a", items); err != nil {
		t.Fatalf("SaveExtractedData: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM people WHERE platform_username LIKE 'urn:%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("urn people = %d (%v), want none", n, err)
	}
	var username, fullName, profileURL string
	if err := db.QueryRow(`SELECT platform_username, full_name, profile_url FROM people WHERE profile_id = 'p1'`).Scan(&username, &fullName, &profileURL); err != nil {
		t.Fatalf("author row: %v", err)
	}
	if username != "ada-first-test" || fullName != "Ada First" || profileURL != "https://www.linkedin.com/in/ada-first-test/" {
		t.Fatalf("author = %s / %s / %s", username, fullName, profileURL)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM people`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("people = %d (%v), want only the member author", n, err)
	}
	// Every post is still a target; only the first is linked (to its author).
	if err := db.QueryRow(`SELECT COUNT(*) FROM workflow_node_targets WHERE node_id = 'cli-node'`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("targets = %d (%v), want 3", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM workflow_node_targets WHERE person_id IS NOT NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("linked targets = %d (%v), want 1", n, err)
	}
}

// A profile read fills the person's profile columns, experience and
// education included.
func TestSaveExtractedDataStoresProfileDetails(t *testing.T) {
	db := newTargetsDB(t)
	s := &workflowActionStorage{db: db, profileID: "p1", executionID: "cli", nodeID: "cli-node", platform: "linkedin"}
	items := []map[string]interface{}{{
		"username": "ada-first-test", "profile_url": "https://www.linkedin.com/in/ada-first-test/",
		"full_name": "Ada First", "headline": "Robotics engineer", "job_title": "Engineer",
		"location": "Lisbon, Portugal", "about": "Builds things.", "profile_picture_url": "https://media.example/ada.jpg",
		"experience": []interface{}{map[string]interface{}{"title": "Engineer", "company": "Example Works"}},
		"education":  []interface{}{map[string]interface{}{"school": "Example University"}},
	}}
	if err := s.SaveExtractedData("a", items); err != nil {
		t.Fatalf("SaveExtractedData: %v", err)
	}
	var fullName, headline, jobTitle, location, about, imageURL, experience, education string
	if err := db.QueryRow(`SELECT full_name, headline, job_title, location, about, image_url, experience, education FROM people WHERE platform_username = 'ada-first-test'`).
		Scan(&fullName, &headline, &jobTitle, &location, &about, &imageURL, &experience, &education); err != nil {
		t.Fatalf("person: %v", err)
	}
	if fullName != "Ada First" || headline != "Robotics engineer" || jobTitle != "Engineer" || location != "Lisbon, Portugal" ||
		about != "Builds things." || imageURL != "https://media.example/ada.jpg" ||
		experience != `[{"company":"Example Works","title":"Engineer"}]` || education != `[{"school":"Example University"}]` {
		t.Fatalf("person = %q %q %q %q %q %q %s %s", fullName, headline, jobTitle, location, about, imageURL, experience, education)
	}
}

// An Instagram/TikTok/X profile read stores the bio as the person's About,
// never as the introduction (the outreach draft the review flow sends),
// and its platform extras in profile_details; a later read merges into
// them and leaves a drafted introduction alone.
func TestSaveExtractedDataBioIsAboutAndExtrasAreDetails(t *testing.T) {
	db := newTargetsDB(t)
	if _, err := db.Exec(`INSERT INTO people (id, platform_username, platform, introduction, category, profile_id)
		VALUES ('p', 'fake_creator', 'TIKTOK', 'Hi, loved your videos', 'pending_approval', 'p1')`); err != nil {
		t.Fatal(err)
	}
	s := &workflowActionStorage{db: db, profileID: "p1", executionID: "cli", nodeID: "cli-node", platform: "tiktok"}
	read := func(item map[string]interface{}) {
		t.Helper()
		if err := s.SaveExtractedData("a", []map[string]interface{}{item}); err != nil {
			t.Fatalf("SaveExtractedData: %v", err)
		}
	}
	read(map[string]interface{}{
		"profile_url": "https://www.tiktok.com/@fake_creator", "username": "fake_creator", "full_name": "Fake Creator",
		"bio": "Synthetic bio", "introduction": "must not be stored", "profile_category": "Artist",
		"likes_count": "5.6K", "language": "en", "is_private": false,
		"links": []interface{}{map[string]interface{}{"url": "https://example.test/fake-link"}},
	})
	read(map[string]interface{}{
		"profile_url": "https://www.tiktok.com/@fake_creator", "username": "fake_creator", "bio": "Synthetic bio",
		"likes_count": "5700",
	})
	var intro, category, about, details string
	if err := db.QueryRow(`SELECT introduction, category, about, profile_details FROM people WHERE id = 'p'`).Scan(&intro, &category, &about, &details); err != nil {
		t.Fatal(err)
	}
	if intro != "Hi, loved your videos" || category != "pending_approval" || about != "Synthetic bio" {
		t.Fatalf("introduction/category/about = %q / %q / %q", intro, category, about)
	}
	var d map[string]interface{}
	if err := json.Unmarshal([]byte(details), &d); err != nil {
		t.Fatalf("details %q: %v", details, err)
	}
	if d["likes_count"] != float64(5700) || d["profile_category"] != "Artist" || d["language"] != "en" || d["is_private"] != false || d["links"] == nil {
		t.Fatalf("details = %s", details)
	}
}

// A profile read keeps its follower, following and post counts.
func TestSaveExtractedDataStoresCounts(t *testing.T) {
	db := newTargetsDB(t)
	s := &workflowActionStorage{db: db, profileID: "p1", executionID: "cli", nodeID: "cli-node", platform: "x"}
	read := func(item map[string]interface{}) {
		t.Helper()
		if err := s.SaveExtractedData("a", []map[string]interface{}{item}); err != nil {
			t.Fatal(err)
		}
	}
	read(map[string]interface{}{"username": "ada", "profile_url": "https://x.com/ada", "followers_count": 176697, "following_count": float64(332), "content_count": "10,168"})
	read(map[string]interface{}{"username": "ada", "profile_url": "https://x.com/ada", "full_name": "Ada"}) // a read without counts keeps them
	read(map[string]interface{}{"username": "kay", "profile_url": "https://x.com/kay", "followers_count": "5.6K"})
	var kay int64
	if err := db.QueryRow(`SELECT follower_count FROM people WHERE platform_username = 'kay'`).Scan(&kay); err != nil || kay != 5600 {
		t.Fatalf("abbreviated count = %d (%v)", kay, err)
	}
	var followers, following, posts int64
	if err := db.QueryRow(`SELECT follower_count, following_count, content_count FROM people WHERE platform_username = 'ada'`).Scan(&followers, &following, &posts); err != nil {
		t.Fatal(err)
	}
	if followers != 176697 || following != 332 || posts != 10168 {
		t.Fatalf("counts = %d %d %d", followers, following, posts)
	}
}

// With no profile the storage must fail loudly: saving or counting would
// otherwise land in a shared "default" bucket (and share its daily caps).
func TestActionStorageWithoutProfileErrors(t *testing.T) {
	db := newTargetsDB(t)
	s := &workflowActionStorage{db: db, executionID: "cli", nodeID: "cli-node", platform: "hackernews"}

	if err := s.SaveExtractedData("a", []map[string]interface{}{{"url": "https://news.ycombinator.com/item?id=1"}}); err == nil {
		t.Error("SaveExtractedData without a profile: want error")
	}
	if _, err := s.IncrementDailyActionCount("like"); err == nil {
		t.Error("IncrementDailyActionCount without a profile: want error")
	}
	if _, err := s.GetDailyActionCount("like"); err == nil {
		t.Error("GetDailyActionCount without a profile: want error")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workflow_daily_counters`).Scan(&n); err != nil || n != 0 {
		t.Errorf("counters rows = %d (%v), want 0", n, err)
	}
}

// The execution is looked up before the first write: a writer that commits in between (the nodes of one run save
// concurrently) must not fail the save with SQLITE_BUSY_SNAPSHOT (#1460). The driver lets one competing writer
// commit right before the person upsert is prepared, after the lookup.
func TestSaveExtractedDataSurvivesAWriterCommittingBetweenItsReadAndItsWrite(t *testing.T) {
	base := newTargetsDB(t)
	var path string
	if err := base.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil || path == "" {
		t.Fatalf("database file: %q, %v", path, err)
	}
	db, trip := dbconntest.OpenPath(t, path)
	competing := dbconntest.CompetingWriter(t, path)
	fired := false
	trip.BeforePrepare = func(q string) {
		if !fired && strings.Contains(q, "INSERT INTO people") {
			fired = true
			competing()
		}
	}
	s := &workflowActionStorage{db: db, profileID: "p1", executionID: "cli", nodeID: "cli-node", platform: "hackernews"}
	items := []map[string]interface{}{{"url": "https://news.ycombinator.com/item?id=1", "username": "someone", "platform": "hackernews"}}
	if err := s.SaveExtractedData("a", items); err != nil {
		t.Fatalf("SaveExtractedData failed when a writer committed between its read and its write: %v", err)
	}
	if !fired {
		t.Fatal("the hook never ran")
	}
	var n int
	if err := base.QueryRow(`SELECT COUNT(*) FROM workflow_node_targets WHERE node_id = 'cli-node'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("targets = %d, %v", n, err)
	}
}
