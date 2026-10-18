package capturetask

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
)

func strptr(s string) *string { return &s }

// seedCapture writes one envelope and returns its directory.
func seedCapture(t *testing.T, meta capture.Meta) string {
	t.Helper()
	w := &capture.Writer{Inbox: filepath.Join(t.TempDir(), "inbox")}
	res, err := w.Write(&capture.Envelope{
		Meta: meta,
		Artifacts: map[string]capture.Artifact{
			capture.ArtifactMHTML:    capture.Inline([]byte("archive bytes")),
			capture.ArtifactReadable: capture.Inline([]byte("# " + meta.Title + "\n\nbody\n")),
		},
	})
	if err != nil {
		t.Fatalf("seed capture: %v", err)
	}
	real, err := filepath.EvalSymlinks(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// orgRoot makes a project root with one org config in it.
func orgRoot(t *testing.T, orgs ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".monomind", "orgs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, org := range orgs {
		if err := os.WriteFile(filepath.Join(dir, org+".json"),
			[]byte(`{"name":"`+org+`","version":2}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// board decodes an org's issue store.
func board(t *testing.T, root, org string) map[string]any {
	t.Helper()
	path, err := IssuesPath(root, org)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read board: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("board is not valid JSON: %v", err)
	}
	return doc
}

func fixedNow() func() time.Time {
	at := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return at }
}

func TestCreateWritesMonomindsIssueShape(t *testing.T) {
	root := orgRoot(t, "acme")
	dir := seedCapture(t, capture.Meta{
		URL: "https://example.com/article", CanonicalURL: "https://example.com/article",
		Title: "An Article", CapturedAt: "2026-09-20T10:00:00Z", Source: "crawl",
		Byline: strptr("A. Writer"), Collection: strptr("research"), Tags: []string{"go"},
	})

	res, err := Create(Options{
		Root: root, Org: "acme", EnvelopePath: dir,
		Description: "Read this before the review.", Priority: "high",
		Assignee: "alice", Workspace: "ws-1", Now: fixedNow(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.IssuesFile != filepath.Join(root, ".monomind", "orgs", "acme-issues.json") {
		t.Errorf("wrote to %s", res.IssuesFile)
	}

	doc := board(t, root, "acme")
	issues, ok := doc["issues"].([]any)
	if !ok || len(issues) != 1 {
		t.Fatalf("board holds %v", doc["issues"])
	}
	issue := issues[0].(map[string]any)

	// The fields monomind's own skills read, in the form they read them.
	for key, want := range map[string]any{
		"status": "todo", "priority": "high", "title": "An Article",
		"assigneeId": "alice", "workspaceId": "ws-1",
	} {
		if issue[key] != want {
			t.Errorf("issue[%q] = %v, want %v", key, issue[key], want)
		}
	}
	for _, key := range []string{"id", "description", "createdAt", "updatedAt", "blockedByIssueIds"} {
		if _, ok := issue[key]; !ok {
			t.Errorf("issue is missing %q", key)
		}
	}
	for _, key := range []string{"parentId", "projectId", "assigneeAgentId", "assigneeUserId"} {
		if v, ok := issue[key]; !ok || v != nil {
			t.Errorf("issue[%q] = %v, want an explicit null", key, v)
		}
	}
	if !strings.HasPrefix(issue["id"].(string), "issue-") {
		t.Errorf("issue id = %v", issue["id"])
	}
	if desc := issue["description"].(string); !strings.Contains(desc, "Read this before the review.") ||
		!strings.Contains(desc, "https://example.com/article") || !strings.Contains(desc, dir) {
		t.Errorf("description does not carry the capture:\n%s", desc)
	}
}

func TestCreateAttachesEveryArtifact(t *testing.T) {
	root := orgRoot(t, "acme")
	dir := seedCapture(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})

	res, err := Create(Options{Root: root, Org: "acme", EnvelopePath: dir, Now: fixedNow()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	names := map[string]Attachment{}
	for _, a := range res.Issue.Attachments {
		names[a.Name] = a
	}
	for _, want := range []string{capture.MetaFile, capture.ArtifactMHTML, capture.ArtifactReadable} {
		a, ok := names[want]
		if !ok {
			t.Fatalf("attachments %v are missing %s", res.Issue.Attachments, want)
		}
		if a.SizeBytes <= 0 || a.AddedAt == "" || !strings.HasPrefix(a.Path, dir) {
			t.Errorf("attachment %s = %+v", want, a)
		}
	}
	if names[capture.ArtifactReadable].Type != "markdown" || names[capture.ArtifactMHTML].Type != "archive" {
		t.Errorf("attachment types = %+v", names)
	}
	if res.Issue.Capture == nil || res.Issue.Capture.Path != dir || res.Issue.Capture.ContentHash == "" {
		t.Errorf("issue.capture = %+v, want a machine-readable link back", res.Issue.Capture)
	}
}

func TestCreatePreservesIssuesItDidNotWrite(t *testing.T) {
	root := orgRoot(t, "acme")
	path, _ := IssuesPath(root, "acme")
	existing := `{
  "issues": [
    {"id": "issue-1", "title": "Older", "status": "in_progress", "somethingNewer": {"a": 1}}
  ],
  "boardSettings": {"columns": ["todo", "doing"]}
}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := seedCapture(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	if _, err := Create(Options{Root: root, Org: "acme", EnvelopePath: dir, Now: fixedNow()}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	doc := board(t, root, "acme")
	if _, ok := doc["boardSettings"]; !ok {
		t.Error("a top-level key this tool does not know was dropped")
	}
	issues := doc["issues"].([]any)
	if len(issues) != 2 {
		t.Fatalf("board holds %d issues, want the old one plus the new", len(issues))
	}
	old := issues[0].(map[string]any)
	if old["id"] != "issue-1" || old["status"] != "in_progress" {
		t.Errorf("existing issue was rewritten: %+v", old)
	}
	if _, ok := old["somethingNewer"]; !ok {
		t.Error("a field this build does not know was dropped from an existing issue")
	}
}

func TestCreateIDsDoNotCollideWithinAMillisecond(t *testing.T) {
	root := orgRoot(t, "acme")
	dir := seedCapture(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	now := fixedNow()

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		res, err := Create(Options{Root: root, Org: "acme", EnvelopePath: dir, Now: now})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		if seen[res.Issue.ID] {
			t.Fatalf("id %s was issued twice", res.Issue.ID)
		}
		seen[res.Issue.ID] = true
	}
}

func TestCreateValidatesItsInputs(t *testing.T) {
	root := orgRoot(t, "acme")
	dir := seedCapture(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})

	// Each case pins the reason as well as the refusal. "err != nil" stays
	// true when a case quietly stops being about what it is named after —
	// "missing capture" would keep passing if that path started failing
	// for some entirely different reason.
	cases := map[string]struct {
		opts Options
		says string
	}{
		"no board":         {Options{Root: root, EnvelopePath: dir}, "no board"},
		"bad org name":     {Options{Root: root, Org: "../escape", EnvelopePath: dir}, "invalid org name"},
		"bad priority":     {Options{Root: root, Org: "acme", EnvelopePath: dir, Priority: "urgentish"}, `priority "urgentish"`},
		"missing capture":  {Options{Root: root, Org: "acme", EnvelopePath: filepath.Join(root, "nope")}, "is not a capture"},
		"unknown parent":   {Options{Root: root, Org: "acme", EnvelopePath: dir, Parent: "issue-does-not-exist"}, "parent issue"},
		"no capture given": {Options{Root: root, Org: "acme"}, "no capture path"},
	}
	for name, tc := range cases {
		opts := tc.opts
		opts.Now = fixedNow()
		_, err := Create(opts)
		if err == nil {
			t.Errorf("%s should have been refused", name)
			continue
		}
		if !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s was refused for the wrong reason: %v", name, err)
		}
	}
	// And no refusal reached the board — not even the one that had to open
	// it to check the parent.
	if _, err := os.Stat(filepath.Join(root, ".monomind", "orgs", "acme-issues.json")); err == nil {
		t.Error("a refused task still wrote the board")
	}
}

func TestCreateAcceptsAKnownParent(t *testing.T) {
	root := orgRoot(t, "acme")
	dir := seedCapture(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	parent, err := Create(Options{Root: root, Org: "acme", EnvelopePath: dir, Now: fixedNow()})
	if err != nil {
		t.Fatalf("Create parent: %v", err)
	}
	child, err := Create(Options{Root: root, Org: "acme", EnvelopePath: dir, Parent: parent.Issue.ID, Now: fixedNow()})
	if err != nil {
		t.Fatalf("Create child: %v", err)
	}
	if child.Issue.ParentID == nil || *child.Issue.ParentID != parent.Issue.ID {
		t.Errorf("child.parentId = %v", child.Issue.ParentID)
	}
}

func TestBoardsListsOrgsAndExistingStores(t *testing.T) {
	root := orgRoot(t, "acme", "beta")
	// An org whose config is gone but whose board is not.
	path := filepath.Join(root, ".monomind", "orgs", "legacy-issues.json")
	if err := os.WriteFile(path, []byte(`{"issues":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Boards(root)
	if err != nil {
		t.Fatalf("Boards: %v", err)
	}
	if strings.Join(got, ",") != "acme,beta,legacy" {
		t.Errorf("Boards = %v", got)
	}
	// An artifact file must not be mistaken for an org.
	if err := os.WriteFile(filepath.Join(root, ".monomind", "orgs", "acme-goals.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := Boards(root); strings.Join(got, ",") != "acme,beta,legacy" {
		t.Errorf("Boards = %v after an unrelated artifact landed", got)
	}
}

func TestBoardsOnAFreshRoot(t *testing.T) {
	got, err := Boards(t.TempDir())
	if err != nil {
		t.Fatalf("Boards: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Boards = %v, want none", got)
	}
}

// The board is a shared document: monomind's dashboard renders it and
// other agents read it. What a capture puts there — an absolute path, a
// title taken from a web page — crosses a trust boundary on the way in.

// TestCreateRecordsTheRealPathNotASymlink: the path on the issue is what a
// reader will open months from now. If it is a symlink, what it points at
// is decided then, not now — so the resolved directory is what is written.
func TestCreateRecordsTheRealPathNotASymlink(t *testing.T) {
	root := orgRoot(t, "acme")
	real := seedCapture(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	link := filepath.Join(t.TempDir(), "link-to-capture")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	res, err := Create(Options{Root: root, Org: "acme", EnvelopePath: link, Now: fixedNow()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if res.Issue.Capture.Path != real {
		t.Errorf("issue.capture.path = %s, want the capture itself (%s)", res.Issue.Capture.Path, real)
	}
	for _, a := range res.Issue.Attachments {
		if filepath.Dir(a.Path) != real {
			t.Errorf("attachment %s sits at %s, want it under %s", a.Name, a.Path, real)
		}
	}
}

// TestCreateRefusesAFileAsACapture: a capture is a directory. Saying so is
// better than the "not a directory" the join would produce.
func TestCreateRefusesAFileAsACapture(t *testing.T) {
	root := orgRoot(t, "acme")
	file := filepath.Join(t.TempDir(), "meta.json")
	if err := os.WriteFile(file, []byte(`{"url":"https://x.test/"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Create(Options{Root: root, Org: "acme", EnvelopePath: file, Now: fixedNow()})
	if err == nil {
		t.Fatal("a file is not a capture")
	}
	if !strings.Contains(err.Error(), "not a capture") {
		t.Errorf("err = %v", err)
	}
}

// TestCreateBoundsAPageChosenTitle: the title comes off a web page, and it
// lands in a file other people's tools read and render. A megabyte of it
// is not a title.
func TestCreateBoundsAPageChosenTitle(t *testing.T) {
	root := orgRoot(t, "acme")
	huge := strings.Repeat("no", 200000)
	dir := seedCapture(t, capture.Meta{URL: "https://example.com/a", Title: huge, CapturedAt: "2026-09-20T10:00:00Z"})

	res, err := Create(Options{Root: root, Org: "acme", EnvelopePath: dir, Now: fixedNow()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n := len([]rune(res.Issue.Title)); n > maxTitleRunes {
		t.Errorf("the issue title is %d runes long", n)
	}
	if !strings.HasPrefix(res.Issue.Title, "nono") {
		t.Errorf("the title was not kept, it was replaced: %q", res.Issue.Title)
	}
	blob, err := os.ReadFile(filepath.Join(root, ".monomind", "orgs", "acme-issues.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) > 1<<20 {
		t.Errorf("one capture wrote a %d byte board", len(blob))
	}
}
