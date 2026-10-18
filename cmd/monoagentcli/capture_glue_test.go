package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
	"github.com/monoes/mono-agent/internal/captureexport"
)

// runGlueCmd executes a `capture` subcommand, returning stdout and stderr
// separately: the machine-readable answer goes to stdout, the commentary to
// stderr, and a test that conflates them cannot tell.
func runGlueCmd(t *testing.T, cfg *globalConfig, args ...string) (string, string, error) {
	t.Helper()
	if cfg == nil {
		cfg = &globalConfig{}
	}
	cmd := newCaptureCmd(cfg)
	cmd.SetArgs(args)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// seedGlueInbox writes captures and returns the inbox holding them.
func seedGlueInbox(t *testing.T, metas ...capture.Meta) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(base, "inbox")
	for _, meta := range metas {
		w := &capture.Writer{Inbox: inbox}
		if _, err := w.Write(&capture.Envelope{
			Meta: meta,
			Artifacts: map[string]capture.Artifact{
				capture.ArtifactMHTML:    capture.Inline([]byte("archive of " + meta.URL)),
				capture.ArtifactReadable: capture.Inline([]byte("# " + meta.Title + "\n\nbody\n")),
			},
		}); err != nil {
			t.Fatalf("seed %s: %v", meta.URL, err)
		}
	}
	return inbox
}

func gluePtr(s string) *string { return &s }

// glueFiles reads a capture directory as name -> bytes. Counting
// directories proves an import created something; only the bytes prove it
// restored a capture.
func glueFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			t.Errorf("%s holds a subdirectory %q; an envelope is flat", dir, e.Name())
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out[e.Name()] = string(body)
	}
	if len(out) == 0 {
		t.Errorf("%s is empty", dir)
	}
	return out
}

// glueSameCapture asserts two envelope directories hold the same files
// with the same bytes.
func glueSameCapture(t *testing.T, want, got string) {
	t.Helper()
	a, b := glueFiles(t, want), glueFiles(t, got)
	if len(a) != len(b) {
		t.Errorf("%s holds %v, %s holds %v", want, sortedNames(a), got, sortedNames(b))
	}
	for name, body := range a {
		switch other, ok := b[name]; {
		case !ok:
			t.Errorf("the restored capture is missing %s", name)
		case other != body:
			t.Errorf("%s differs: %q became %q", name, body, other)
		}
	}
}

func sortedNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// byDirName indexes captures by their envelope directory name.
func byDirName(entries []capture.Entry) map[string]capture.Entry {
	out := make(map[string]capture.Entry, len(entries))
	for _, e := range entries {
		out[filepath.Base(e.Path)] = e
	}
	return out
}

func TestCaptureExportImportRoundTripThroughTheCLI(t *testing.T) {
	src := seedGlueInbox(t,
		capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"},
		capture.Meta{URL: "https://example.com/b", Title: "B", CapturedAt: "2026-09-21T10:00:00Z"},
	)
	archive := filepath.Join(t.TempDir(), "reading.tar.gz")

	stdout, stderr, err := runGlueCmd(t, nil, "export", "--inbox", src, "--out", archive)
	if err != nil {
		t.Fatalf("capture export: %v\n%s", err, stderr)
	}
	if strings.TrimSpace(stdout) != archive {
		t.Errorf("export printed %q, want the archive path on stdout", stdout)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("archive was not written: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "inbox")
	if _, stderr, err = runGlueCmd(t, nil, "import", archive, "--inbox", dst); err != nil {
		t.Fatalf("capture import: %v\n%s", err, stderr)
	}
	entries, err := capture.List(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("restored %d captures, want 2", len(entries))
	}

	// Counting directories would pass for an import that wrote two
	// meta.json files and dropped every artifact. Compare the bytes.
	before, err := capture.List(src)
	if err != nil {
		t.Fatal(err)
	}
	restored := byDirName(entries)
	for _, want := range before {
		got, ok := restored[filepath.Base(want.Path)]
		if !ok {
			t.Fatalf("%s did not come back; the inbox holds %v", filepath.Base(want.Path), sortedKeysOf(restored))
		}
		if got.URL != want.URL || got.Title != want.Title || got.CapturedAt != want.CapturedAt {
			t.Errorf("%+v came back as %+v", want, got)
		}
		glueSameCapture(t, want.Path, got.Path)
	}
}

func sortedKeysOf(m map[string]capture.Entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestCaptureExportFiltersAndJSON(t *testing.T) {
	src := seedGlueInbox(t,
		capture.Meta{URL: "https://example.com/old", Title: "Old", CapturedAt: "2026-09-01T10:00:00Z"},
		capture.Meta{URL: "https://example.com/new", Title: "New", CapturedAt: "2026-09-20T10:00:00Z",
			Collection: gluePtr("research")},
	)
	archive := filepath.Join(t.TempDir(), "research.tar.gz")

	stdout, stderr, err := runGlueCmd(t, &globalConfig{JSONOutput: true},
		"export", "--inbox", src, "--out", archive, "--collection", "research", "--since", "2026-09-10")
	if err != nil {
		t.Fatalf("capture export: %v\n%s", err, stderr)
	}
	var got struct {
		Archive  string                 `json:"archive"`
		Manifest captureexport.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if got.Archive != archive || got.Manifest.Count != 1 {
		t.Fatalf("export = %+v", got)
	}
	if got.Manifest.Captures[0].Title != "New" {
		t.Errorf("exported the wrong capture: %+v", got.Manifest.Captures[0])
	}
}

func TestCaptureExportRejectsABadDate(t *testing.T) {
	// --since is refused before anything is read or written, so this test
	// names neither an inbox nor an output — which would make a regression
	// read the developer's real ~/.monomind/inbox and drop a tarball in
	// the repo. Point both somewhere disposable so it cannot, whatever the
	// flag parsing does later.
	home := t.TempDir()
	t.Setenv(capture.InboxEnv, filepath.Join(home, "inbox"))
	t.Setenv(capture.HomeEnv, home)
	t.Chdir(home)

	_, _, err := runGlueCmd(t, nil, "export", "--since", "whenever")
	if err == nil {
		t.Fatal("an unparseable --since should be refused")
	}
	if code := exitCodeFor(err); code != 3 {
		t.Errorf("exit code %d, want 3 (invalid input)", code)
	}
	left, _ := os.ReadDir(home)
	for _, e := range left {
		t.Errorf("a refused export wrote %s", e.Name())
	}
}

func TestCaptureImportCollisionModes(t *testing.T) {
	src := seedGlueInbox(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	archive := filepath.Join(t.TempDir(), "one.tar.gz")
	if _, stderr, err := runGlueCmd(t, nil, "export", "--inbox", src, "--out", archive); err != nil {
		t.Fatalf("export: %v\n%s", err, stderr)
	}
	dst := filepath.Join(t.TempDir(), "inbox")
	original, _ := capture.List(src)
	name := filepath.Base(original[0].Path)

	if _, _, err := runGlueCmd(t, nil, "import", archive, "--inbox", dst); err != nil {
		t.Fatalf("first import: %v", err)
	}
	// Edit the local copy: skip and rename must both leave it alone, and a
	// count cannot tell whether they did.
	landed := filepath.Join(dst, name)
	marker := filepath.Join(landed, capture.ArtifactReadable)
	if err := os.WriteFile(marker, []byte("edited locally"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Default is skip: a second import changes nothing.
	if _, _, err := runGlueCmd(t, nil, "import", archive, "--inbox", dst); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if entries, _ := capture.List(dst); len(entries) != 1 {
		t.Fatalf("skip left %d captures, want 1", len(entries))
	}
	if body, _ := os.ReadFile(marker); string(body) != "edited locally" {
		t.Errorf("skip rewrote the local capture: %q", body)
	}

	// Rename keeps both: the local edit where it was, the archive's copy
	// beside it under a new name.
	if _, _, err := runGlueCmd(t, nil, "import", archive, "--inbox", dst, "--on-collision", "rename"); err != nil {
		t.Fatalf("rename import: %v", err)
	}
	entries, _ := capture.List(dst)
	if len(entries) != 2 {
		t.Fatalf("rename left %d captures, want 2", len(entries))
	}
	if body, _ := os.ReadFile(marker); string(body) != "edited locally" {
		t.Errorf("rename overwrote the local capture instead of keeping both: %q", body)
	}
	renamed := byDirName(entries)[name+"-2"]
	if renamed.Path == "" {
		t.Fatalf("no renamed copy in %v", sortedKeysOf(byDirName(entries)))
	}
	glueSameCapture(t, original[0].Path, renamed.Path)

	// Overwrite replaces the local copy with the archive's, edit and all.
	if _, _, err := runGlueCmd(t, nil, "import", archive, "--inbox", dst, "--on-collision", "overwrite"); err != nil {
		t.Fatalf("overwrite import: %v", err)
	}
	if entries, _ := capture.List(dst); len(entries) != 2 {
		t.Fatalf("overwrite left %d captures, want 2", len(entries))
	}
	glueSameCapture(t, original[0].Path, landed)

	if _, _, err := runGlueCmd(t, nil, "import", archive, "--inbox", dst, "--on-collision", "merge"); err == nil {
		t.Error("an unknown collision mode should be refused")
	}
}

func TestCaptureImportDryRun(t *testing.T) {
	src := seedGlueInbox(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	archive := filepath.Join(t.TempDir(), "one.tar.gz")
	if _, _, err := runGlueCmd(t, nil, "export", "--inbox", src, "--out", archive); err != nil {
		t.Fatalf("export: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "inbox")

	_, stderr, err := runGlueCmd(t, nil, "import", archive, "--inbox", dst, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(stderr, "Would import") {
		t.Errorf("a dry run should say it did not write:\n%s", stderr)
	}
	if entries, _ := capture.List(dst); len(entries) != 0 {
		t.Errorf("a dry run wrote %d captures", len(entries))
	}
}

func TestCaptureImportMissingArchive(t *testing.T) {
	_, _, err := runGlueCmd(t, nil, "import", filepath.Join(t.TempDir(), "nope.tar.gz"))
	if err == nil {
		t.Fatal("a missing archive should be an error")
	}
	if code := exitCodeFor(err); code != 2 {
		t.Errorf("exit code %d, want 2 (not found)", code)
	}
}

func TestDefaultArchiveNameIsSortable(t *testing.T) {
	got := defaultArchiveName(time.Date(2026, 9, 21, 12, 30, 45, 0, time.UTC))
	if got != "captures-20260921-123045.tar.gz" {
		t.Errorf("defaultArchiveName = %q", got)
	}
}

// failAfter writes n bytes and then refuses, standing in for the far end of
// `capture export --out - | ssh other-box ...` going away.
type failAfter struct {
	n    int
	seen int
}

func (f *failAfter) Write(p []byte) (int, error) {
	if f.seen >= f.n {
		return 0, errors.New("broken pipe")
	}
	f.seen += len(p)
	return len(p), nil
}

// The file path stages the archive and renames it, so an interrupted
// export never leaves something that looks like a backup. A pipe cannot be
// staged — the bytes are gone the moment they are written — so the one
// thing this can do is say so, loudly, rather than let a truncated stream
// be mistaken for an archive.
func TestCaptureExportToStdoutSaysWhenTheStreamIsTruncated(t *testing.T) {
	src := seedGlueInbox(t,
		capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"},
		capture.Meta{URL: "https://example.com/b", Title: "B", CapturedAt: "2026-09-21T10:00:00Z"},
	)

	cmd := newCaptureCmd(&globalConfig{})
	cmd.SetArgs([]string{"export", "--inbox", src, "--out", "-"})
	var stderr bytes.Buffer
	cmd.SetOut(&failAfter{n: 1})
	cmd.SetErr(&stderr)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("a pipe that went away should be an error")
	}
	if !strings.Contains(stderr.String(), "not a complete archive") {
		t.Errorf("the truncated stream was not called out:\n%s", stderr.String())
	}
}
