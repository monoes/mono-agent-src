package people

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// seedCRM adds, on top of newReviewCLITestDB's p1/p2 (LINKEDIN, "Sam"), a
// person in another profile, a workflow interaction, posts, a comment and
// tags — the things the app's People and Profile pages read.
func seedCRM(t *testing.T, db *storage.Database) {
	t.Helper()
	for _, q := range []string{
		`UPDATE people SET profile_url = 'https://li/p1', created_at = '2026-09-01 10:00:00' WHERE id = 'p1'`,
		`UPDATE people SET created_at = '2026-09-02 10:00:00', full_name = 'Alex' WHERE id = 'p2'`,
		`INSERT INTO people (id, profile_id, platform, platform_username, full_name) VALUES ('x1', 'other', 'LINKEDIN', 'x1-user', 'Sam')`,
		`INSERT INTO workflows (id, name, profile_id) VALUES ('w1', 'Outreach', 'default')`,
		`INSERT INTO workflows (id, name, profile_id) VALUES ('w2', 'Theirs', 'other')`,
		`INSERT INTO workflow_executions (id, workflow_id, status) VALUES ('e1', 'w1', 'success')`,
		`INSERT INTO workflow_executions (id, workflow_id, status) VALUES ('e2', 'w2', 'success')`,
		`INSERT INTO workflow_node_targets (id, execution_id, node_id, person_id, platform, link, status, comment_text, last_interacted_at)
			VALUES ('t1', 'e1', 'n1', 'p1', 'LINKEDIN', 'https://li/p1', 'COMPLETED', 'hi', '2026-09-03 10:00:00')`,
		`INSERT INTO workflow_node_targets (id, execution_id, node_id, person_id, platform, status)
			VALUES ('t2', 'e2', 'n1', 'p1', 'LINKEDIN', 'COMPLETED')`,
		`INSERT INTO posts (id, person_id, platform, shortcode, url, like_count, caption, scraped_at)
			VALUES ('po1', 'p1', 'instagram', 'abc', 'https://ig/p/abc/', 5, 'first', '2026-09-04')`,
		`INSERT INTO posts (id, person_id, platform, shortcode, url, scraped_at)
			VALUES ('po2', 'p1', 'instagram', 'def', 'https://ig/p/def', '2026-09-05')`,
		`INSERT INTO posts (id, person_id, platform, shortcode, url, scraped_at)
			VALUES ('pox', 'x1', 'instagram', 'xyz', 'https://ig/p/xyz', '2026-09-05')`,
		`INSERT INTO workflow_nodes (id, workflow_id, node_type, name) VALUES ('n1', 'w1', 'instagram.like_posts', 'Like')`,
		`INSERT INTO workflow_nodes (id, workflow_id, node_type, name) VALUES ('n2', 'w2', 'instagram.comment_on_posts', 'Comment')`,
		`INSERT INTO workflow_node_targets (id, execution_id, node_id, platform, link, status)
			VALUES ('t3', 'e1', 'n1', 'INSTAGRAM', 'https://ig/p/abc', 'COMPLETED')`,
		// Another profile's comment on the same post must not count as ours.
		`INSERT INTO workflow_node_targets (id, execution_id, node_id, platform, link, status)
			VALUES ('t4', 'e2', 'n2', 'INSTAGRAM', 'https://ig/p/abc/', 'COMPLETED')`,
		`INSERT INTO post_comments (id, post_id, author, text, timestamp, likes_count, scraped_at)
			VALUES ('c2', 'po1', 'bo', 'second', '2026-09-04T12:00:00Z', 1, '2026-09-04')`,
		`INSERT INTO post_comments (id, post_id, author, text, timestamp, scraped_at)
			VALUES ('c1', 'po1', 'al', 'first', '2026-09-04T11:00:00Z', '2026-09-04')`,
	} {
		if _, err := db.DB.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func decodeJSON[T any](t *testing.T, out string, err error) T {
	t.Helper()
	var v T
	if err != nil {
		t.Fatalf("command failed: %v (%q)", err, out)
	}
	if jerr := json.Unmarshal([]byte(out), &v); jerr != nil {
		t.Fatalf("decoding %q: %v", out, jerr)
	}
	return v
}

func TestPeopleListFiltersAndPages(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	seedCRM(t, db)
	type row struct {
		ID         string `json:"id"`
		Username   string `json:"platform_username"`
		ProfileURL string `json:"profile_url"`
		CreatedAt  string `json:"created_at"`
	}
	ids := func(args ...string) []string {
		out, err := runPeople(t, cfg, append([]string{"list"}, args...)...)
		var got []string
		for _, r := range decodeJSON[[]row](t, out, err) {
			got = append(got, r.ID)
		}
		return got
	}
	if got := ids(); len(got) != 2 || got[0] != "p2" || got[1] != "p1" {
		t.Fatalf("list = %v, want newest first and only this profile", got)
	}
	if got := ids("--platform", "linkedin"); len(got) != 2 {
		t.Fatalf("--platform is case-insensitive: %v", got)
	}
	if got := ids("--platform", "instagram"); len(got) != 0 {
		t.Fatalf("--platform instagram = %v", got)
	}
	if got := ids("--search", "sam"); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("--search sam = %v", got)
	}
	if got := ids("--search", "p2-us"); len(got) != 1 || got[0] != "p2" {
		t.Fatalf("--search by username = %v", got)
	}
	if got := ids("--limit", "1", "--offset", "1"); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("page 2 = %v", got)
	}
	if got := ids("--limit", "0", "--offset", "1"); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("offset without limit = %v", got)
	}
	out, err := runPeople(t, cfg, "list", "--search", "nobody")
	if err != nil || out != "[]\n" {
		t.Fatalf("empty list = %q, %v", out, err)
	}
	out, err = runPeople(t, cfg, "list", "--search", "sam")
	if r := decodeJSON[[]row](t, out, err); r[0].ProfileURL != "https://li/p1" || r[0].CreatedAt == "" || r[0].Username != "p1-user" {
		t.Fatalf("list row = %+v", r[0])
	}
	if _, err := runPeople(t, cfg, "list", "--offset", "-1"); exitCode(err) != 3 {
		t.Fatalf("negative offset: exit %d", exitCode(err))
	}

	for _, c := range []struct {
		args []string
		want int
	}{
		{nil, 2},
		{[]string{"--platform", "LinkedIn"}, 2},
		{[]string{"--search", "alex"}, 1},
		{[]string{"--platform", "x"}, 0},
	} {
		out, err := runPeople(t, cfg, append([]string{"count"}, c.args...)...)
		if got := decodeJSON[map[string]int](t, out, err)["count"]; got != c.want {
			t.Errorf("count %v = %d, want %d", c.args, got, c.want)
		}
	}
}

func TestPeopleGetProfileURLAndNotFound(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	seedCRM(t, db)
	out, err := runPeople(t, cfg, "get", "p1")
	if p := decodeJSON[map[string]any](t, out, err); p["profile_url"] != "https://li/p1" || p["platform_username"] != "p1-user" {
		t.Fatalf("get = %v", p)
	}
	for _, id := range []string{"nope", "x1"} {
		if _, err := runPeople(t, cfg, "get", id); exitCode(err) != 2 {
			t.Errorf("get %s: exit %d, want 2", id, exitCode(err))
		}
	}
}

func TestPeopleInteractionsAndPosts(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	seedCRM(t, db)

	out, err := runPeople(t, cfg, "interactions", "p1")
	in := decodeJSON[[]personInteraction](t, out, err)
	if len(in) != 1 || in[0].ExecutionID != "e1" || in[0].CommentText != "hi" || in[0].Link != "https://li/p1" || in[0].NodeName != "Like" {
		t.Fatalf("interactions = %+v (the other profile's workflow run must not show)", in)
	}
	if out, err := runPeople(t, cfg, "interactions", "p2"); err != nil || out != "[]\n" {
		t.Fatalf("no interactions = %q, %v", out, err)
	}

	out, err = runPeople(t, cfg, "posts", "list", "p1")
	posts := decodeJSON[[]postSummary](t, out, err)
	if len(posts) != 2 || posts[0].ID != "po2" || posts[1].ID != "po1" {
		t.Fatalf("posts = %+v, want newest scrape first", posts)
	}
	if !posts[1].WeLiked || posts[1].WeCommented || posts[0].WeLiked || posts[1].LikeCount != 5 {
		t.Fatalf("liked/commented flags = %+v", posts)
	}
	if out, err := runPeople(t, cfg, "posts", "list", "x1"); err != nil || out != "[]\n" {
		t.Fatalf("another profile's posts = %q, %v", out, err)
	}

	out, err = runPeople(t, cfg, "posts", "get", "po1")
	if p := decodeJSON[map[string]any](t, out, err); p["caption"] != "first" || p["shortcode"] != "abc" {
		t.Fatalf("posts get = %v", p)
	} else if _, has := p["we_liked"]; has {
		t.Fatalf("posts get carries list-only flags: %v", p)
	}
	for _, id := range []string{"nope", "pox"} {
		if _, err := runPeople(t, cfg, "posts", "get", id); exitCode(err) != 2 {
			t.Errorf("posts get %s: exit %d, want 2", id, exitCode(err))
		}
	}

	out, err = runPeople(t, cfg, "posts", "comments", "po1")
	cs := decodeJSON[[]postComment](t, out, err)
	if len(cs) != 2 || cs[0].ID != "c1" || cs[1].LikesCount != 1 {
		t.Fatalf("comments = %+v, want oldest first", cs)
	}
	if out, err := runPeople(t, cfg, "posts", "comments", "pox"); err != nil || out != "[]\n" {
		t.Fatalf("another profile's comments = %q, %v", out, err)
	}
}

func TestPeopleTagMap(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	seedCRM(t, db)
	for _, a := range [][]string{{"p1", "zeta"}, {"p1", "Alpha"}, {"p2", "zeta"}} {
		if _, err := runPeople(t, cfg, "tag", "add", a[0], a[1]); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runPeople(t, cfg, "tag", "map", "p1", "p2", "x1", "nope")
	m := decodeJSON[map[string][]struct{ Name string }](t, out, err)
	if len(m) != 2 || len(m["p1"]) != 2 || m["p1"][0].Name != "Alpha" || m["p2"][0].Name != "zeta" {
		t.Fatalf("tag map = %+v", m)
	}
	if out, err := runPeople(t, cfg, "tag", "map", "p1", "nope"); err != nil {
		t.Fatal(err)
	} else if m := decodeJSON[map[string]any](t, out, nil); len(m) != 1 {
		t.Fatalf("tag map without p2 = %v", m)
	}
	if out, err := runPeople(t, cfg, "tag", "map", "nope"); err != nil || out != "{}\n" {
		t.Fatalf("tag map of nobody = %q, %v", out, err)
	}
}

func TestPeopleMessagesAndStatusJSON(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)

	out, err := runPeople(t, cfg, "messages", "drafts")
	if err != nil || out != "[]\n" {
		t.Fatalf("no drafts = %q, %v", out, err)
	}
	out, err = runPeople(t, cfg, "status", "history", "p1")
	if err != nil || out != "[]\n" {
		t.Fatalf("no status history = %q, %v", out, err)
	}

	out, err = runPeople(t, cfg, "messages", "add", "p1", "--source", "manual", "--subject", "Hi", "--body", "hello")
	msg := decodeJSON[storage.PersonMessage](t, out, err)
	if msg.ID == "" || msg.PersonID != "p1" || msg.Direction != "inbound" || msg.Subject != "Hi" {
		t.Fatalf("messages add = %+v", msg)
	}

	// A draft without a recorded connection is discarded locally only.
	if err := db.UpsertPersonMessage(&storage.PersonMessage{ID: "d1", PersonID: "p1", Source: "outlook", Direction: "outbound", Status: "draft"}, "default"); err != nil {
		t.Fatal(err)
	}
	out, err = runPeople(t, cfg, "messages", "drafts")
	if d := decodeJSON[[]storage.PersonMessageWithPerson](t, out, err); len(d) != 1 || d[0].ID != "d1" {
		t.Fatalf("drafts = %+v", d)
	}
	if _, err := runPeople(t, cfg, "messages", "reject-draft", msg.ID); err == nil {
		t.Fatal("reject-draft discarded a message that is not a draft")
	}
	out, err = runPeople(t, cfg, "messages", "reject-draft", "d1")
	if r := decodeJSON[map[string]string](t, out, err); r["id"] != "d1" || r["status"] != "discarded" {
		t.Fatalf("reject-draft = %v", r)
	}
	if m, _ := db.GetPersonMessage("d1"); m != nil {
		t.Fatalf("draft still stored: %+v", m)
	}
}

// `people get` shows what a social profile read stored: the bio as about
// (the introduction is the draft, labelled as such), and profile_details
// as an object in --json and as labelled rows in the table.
func TestPeopleGetProfileDetails(t *testing.T) {
	cfg, db := newReviewCLITestDB(t)
	if _, err := db.DB.Exec(`UPDATE people SET platform = 'INSTAGRAM', about = 'Synthetic bio', introduction = 'Hi Sam',
		profile_details = '{"links":[{"url":"https://example.test/a","title":"Shop"}],"pronouns":["they/them"],"likes_count":5600,"is_private":false,"pinned_post":{"url":"https://x.test/s/1","text":"Hello"}}'
		WHERE id = 'p1'`); err != nil {
		t.Fatal(err)
	}
	out, err := runPeople(t, cfg, "get", "p1")
	p := decodeJSON[map[string]any](t, out, err)
	d, _ := p["profile_details"].(map[string]any)
	if p["about"] != "Synthetic bio" || p["introduction"] != "Hi Sam" || d["likes_count"] != float64(5600) || d["pronouns"] == nil {
		t.Fatalf("get = %v", p)
	}

	cfg.JSONOutput = false
	out, err = runPeople(t, cfg, "get", "p1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Synthetic bio", "Introduction", "Hi Sam", "Shop · https://example.test/a", "they/them", "5600", "Private", "Hello · https://x.test/s/1"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
}
