package capturecli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/capturetask"
)

// `capture task` through the CLI: the board it picks, the issue it writes
// into monomind's own file, and the refusals. The archive half of the glue
// lives in glue_test.go.

// glueOrgRoot makes a project root with the named orgs configured.
func glueOrgRoot(t *testing.T, orgs ...string) string {
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

func TestCaptureTaskFilesOnTheBoard(t *testing.T) {
	root := glueOrgRoot(t, "acme")
	inbox := seedGlueInbox(t, capture.Meta{
		URL: "https://example.com/article", Title: "An Article",
		CapturedAt: "2026-09-20T10:00:00Z", Source: "crawl",
	})
	entries, err := capture.List(inbox)
	if err != nil || len(entries) != 1 {
		t.Fatalf("seed: %v %v", entries, err)
	}

	// The command records the symlink-resolved capture path (macOS temp dirs
	// are /var -> /private/var), so compare against the real one.
	if real, err := filepath.EvalSymlinks(entries[0].Path); err == nil {
		entries[0].Path = real
	}

	stdout, stderr, err := runGlueCmd(t, &testConfig{JSONOutput: true},
		"task", entries[0].Path, "--project", root, "--board", "acme",
		"--title", "Answer this", "--priority", "high")
	if err != nil {
		t.Fatalf("capture task: %v\n%s", err, stderr)
	}
	var res capturetask.Result
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if res.Org != "acme" || res.Issue.Title != "Answer this" || res.Issue.Priority != "high" {
		t.Fatalf("issue = %+v", res.Issue)
	}
	if res.Issue.Capture == nil || res.Issue.Capture.Path != entries[0].Path {
		t.Errorf("issue.capture = %+v", res.Issue.Capture)
	}
	if len(res.Issue.Attachments) == 0 {
		t.Error("the capture should be attached, not only mentioned")
	}

	// Every file of the envelope is attached, with the path it actually
	// sits at — an attachment list that named the wrong capture would pass
	// a count.
	onDisk := glueFiles(t, entries[0].Path)
	attached := map[string]bool{}
	for _, a := range res.Issue.Attachments {
		attached[a.Name] = true
		if a.Path != filepath.Join(entries[0].Path, a.Name) {
			t.Errorf("attachment %s points at %s", a.Name, a.Path)
		}
		if info, err := os.Stat(a.Path); err != nil {
			t.Errorf("attachment %s is not a file: %v", a.Name, err)
		} else if info.Size() != a.SizeBytes {
			t.Errorf("attachment %s says %d bytes, the file is %d", a.Name, a.SizeBytes, info.Size())
		}
	}
	for name := range onDisk {
		if !attached[name] {
			t.Errorf("%s is in the capture but not on the task", name)
		}
	}
	if !strings.Contains(res.Issue.Description, "https://example.com/article") {
		t.Errorf("the task body does not say where the capture came from:\n%s", res.Issue.Description)
	}

	// The board is real JSON on disk where monomind's own tooling reads it,
	// and it holds the issue that was just reported, not merely an issue.
	blob, err := os.ReadFile(filepath.Join(root, ".monomind", "orgs", "acme-issues.json"))
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	var doc struct {
		Issues []map[string]any `json:"issues"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("board is not valid JSON: %v", err)
	}
	if len(doc.Issues) != 1 {
		t.Fatalf("board holds %d issues, want 1", len(doc.Issues))
	}
	filed := doc.Issues[0]
	for key, want := range map[string]any{
		"id": res.Issue.ID, "status": "todo", "priority": "high", "title": "Answer this",
	} {
		if filed[key] != want {
			t.Errorf("board issue[%q] = %v, want %v", key, filed[key], want)
		}
	}
	ref, ok := filed["capture"].(map[string]any)
	if !ok || ref["path"] != entries[0].Path {
		t.Errorf("the board issue does not point back at the capture: %v", filed["capture"])
	}
	if list, ok := filed["attachments"].([]any); !ok || len(list) != len(onDisk) {
		t.Errorf("the board issue carries %v, the capture holds %d files", filed["attachments"], len(onDisk))
	}
}

func TestCaptureTaskPicksTheOnlyBoard(t *testing.T) {
	root := glueOrgRoot(t, "solo")
	inbox := seedGlueInbox(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	entries, _ := capture.List(inbox)

	if _, stderr, err := runGlueCmd(t, nil, "task", entries[0].Path, "--project", root); err != nil {
		t.Fatalf("capture task: %v\n%s", err, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, ".monomind", "orgs", "solo-issues.json")); err != nil {
		t.Errorf("the only board should have been chosen: %v", err)
	}
}

func TestCaptureTaskRefusesToGuessBetweenBoards(t *testing.T) {
	root := glueOrgRoot(t, "one", "two")
	inbox := seedGlueInbox(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	entries, _ := capture.List(inbox)

	_, _, err := runGlueCmd(t, nil, "task", entries[0].Path, "--project", root)
	if err == nil {
		t.Fatal("two boards and no --board should be refused")
	}
	if code := exitCodeFor(err); code != 3 {
		t.Errorf("exit code %d, want 3", code)
	}
	if !strings.Contains(err.Error(), "one, two") {
		t.Errorf("the error should name the boards: %v", err)
	}
}

func TestCaptureTaskWithNoBoardsAtAll(t *testing.T) {
	root := t.TempDir()
	inbox := seedGlueInbox(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	entries, _ := capture.List(inbox)

	_, _, err := runGlueCmd(t, nil, "task", entries[0].Path, "--project", root)
	if err == nil {
		t.Fatal("a root with no orgs should be an error")
	}
	if code := exitCodeFor(err); code != 2 {
		t.Errorf("exit code %d, want 2 (not found)", code)
	}
}

func TestCaptureTaskRejectsANonCapture(t *testing.T) {
	root := glueOrgRoot(t, "acme")
	notACapture := t.TempDir()

	_, _, err := runGlueCmd(t, nil, "task", notACapture, "--project", root, "--board", "acme")
	if err == nil {
		t.Fatal("a directory with no meta.json is not a capture")
	}
	if code := exitCodeFor(err); code != 3 {
		t.Errorf("exit code %d, want 3", code)
	}
}
