package captureexport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/monoes/mono-agent/internal/capture"
)

// What an import is allowed to do to a disk: how much of it, how many
// inodes, and — for a dry run — none of it at all. These tests watch the
// inbox while the import runs, because every one of them is invisible once
// Import has returned and tidied up after itself.

// watchInbox wraps an archive so the inbox can be examined *while* it is
// being read. Everything these tests are about — a dry run that writes,
// staging directories piling up — is invisible once Import has returned and
// tidied up after itself.
type watchInbox struct {
	r     io.Reader
	inbox string
	// files is every path ever seen inside the inbox, and staging the
	// highest number of staging directories seen at one time.
	files   map[string]bool
	staging int
}

func (w *watchInbox) Read(p []byte) (int, error) {
	w.look()
	return w.r.Read(p)
}

func (w *watchInbox) look() {
	if w.files == nil {
		w.files = map[string]bool{}
	}
	open := 0
	_ = filepath.WalkDir(w.inbox, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(w.inbox, path)
		if relErr != nil || rel == "." {
			return nil
		}
		w.files[rel] = true
		if d.IsDir() && strings.HasPrefix(d.Name(), ".tmp-import-") {
			open++
		}
		return nil
	})
	if open > w.staging {
		w.staging = open
	}
}

// watched reads blob through a watcher that polls inbox on every Read.
func watched(t *testing.T, blob []byte, inbox string, opts ImportOptions) (*ImportResult, *watchInbox) {
	t.Helper()
	if err := os.MkdirAll(inbox, 0o700); err != nil {
		t.Fatal(err)
	}
	w := &watchInbox{r: iotest.OneByteReader(bytes.NewReader(blob)), inbox: inbox}
	res, err := Import(w, opts)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	w.look()
	return res, w
}

// A dry run says it writes nothing. Checking the inbox after Import has
// returned cannot tell: the old implementation unpacked every byte and then
// deleted it, which looks identical from there.
func TestImportDryRunNeverTouchesTheDisk(t *testing.T) {
	src := seed(t,
		capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"},
		capture.Meta{URL: "https://example.com/b", Title: "B", CapturedAt: "2026-09-21T10:00:00Z"},
	)
	blob, _ := exportToBytes(t, ExportOptions{Inbox: src})
	dst := filepath.Join(t.TempDir(), "inbox")

	res, w := watched(t, blob, dst, ImportOptions{Inbox: dst, DryRun: true})
	if len(res.Imported) != 2 {
		t.Fatalf("a dry run should still report what it would do, got %+v", res.Imported)
	}
	if len(w.files) != 0 {
		t.Errorf("a dry run wrote %v", sortedKeys(w.files))
	}
	if after, _ := capture.List(dst); len(after) != 0 {
		t.Errorf("a dry run left %d captures behind", len(after))
	}
	// It still has to know what it is reporting on.
	for _, imp := range res.Imported {
		if imp.URL == "" || imp.Bytes == 0 {
			t.Errorf("dry run reported %+v with nothing in it", imp)
		}
	}
}

// One staging directory per capture, all held until the end, is an inode
// exhaustion an archive of one-byte captures can reach while passing every
// size check.
func TestImportStagesOneCaptureAtATime(t *testing.T) {
	const n = 40
	metas := make([]capture.Meta, 0, n)
	for i := range n {
		metas = append(metas, capture.Meta{
			URL:        fmt.Sprintf("https://example.com/%d", i),
			Title:      fmt.Sprintf("Page %d", i),
			CapturedAt: "2026-09-20T10:00:00Z",
		})
	}
	src := seed(t, metas...)
	blob, _ := exportToBytes(t, ExportOptions{Inbox: src})
	dst := filepath.Join(t.TempDir(), "inbox")

	res, w := watched(t, blob, dst, ImportOptions{Inbox: dst})
	if len(res.Imported) != n {
		t.Fatalf("imported %d of %d captures", len(res.Imported), n)
	}
	if w.staging > 1 {
		t.Errorf("%d staging directories were open at once; a capture should be published as it completes", w.staging)
	}
}

// An archive can declare more captures than any inbox should take in one
// go. The count is what has to be bounded — a million one-byte captures
// weigh nothing and cost an inode each.
func TestImportRefusesTooManyCaptures(t *testing.T) {
	man, _ := json.Marshal(Manifest{Format: Format, Version: Version})
	blob := buildArchive(t, func(tw *tar.Writer) {
		if err := writeFileEntry(tw, ManifestPath, man, zeroTime()); err != nil {
			t.Fatal(err)
		}
		for i := range maxCaptures + 10 {
			name := fmt.Sprintf("captures/2026-09-20T10-00-00Z-%06d/meta.json", i)
			if err := writeFileEntry(tw, name, []byte(`{"url":"https://x.test/"}`), zeroTime()); err != nil {
				t.Fatal(err)
			}
		}
	})
	dst := filepath.Join(t.TempDir(), "inbox")
	_, err := Import(bytes.NewReader(blob), ImportOptions{Inbox: dst})
	if err == nil {
		t.Fatal("an archive past the capture limit should be refused")
	}
	if !strings.Contains(err.Error(), "captures") {
		t.Errorf("the refusal should say what the limit is: %v", err)
	}
	// Captures are published as they complete, so the ones under the limit
	// are in the inbox and keep their contents — but the limit is what the
	// inbox grew by, and not one directory more.
	left, err := os.ReadDir(dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) > maxCaptures {
		t.Errorf("the inbox gained %d directories, past the %d limit", len(left), maxCaptures)
	}
}

// The limits are guardrails, not formalities: an import of untrusted data
// into a home directory must not be allowed to fill the disk.
func TestImportLimitsAreDefensible(t *testing.T) {
	if maxFileBytes > 1<<30 {
		t.Errorf("maxFileBytes = %d; one artifact past a GiB is not a capture", maxFileBytes)
	}
	if maxTotalBytes > 16<<30 {
		t.Errorf("maxTotalBytes = %d; that is a disk-fill, not a limit", maxTotalBytes)
	}
	man, _ := json.Marshal(Manifest{Format: Format, Version: Version})
	// A tar header may claim any size it likes, and the bytes need not
	// follow: the claim alone is refused, before a byte of it is read.
	// (The archive is therefore truncated, which is why it is not built
	// through buildArchive — a tar.Writer refuses to close one.)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := writeFileEntry(tw, ManifestPath, man, zeroTime()); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: "captures/big/readable.md",
		Size: maxFileBytes + 1, Mode: 0o600, ModTime: zeroTime(), Format: tar.FormatPAX,
	}); err != nil {
		t.Fatal(err)
	}
	_ = tw.Flush()
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "inbox")
	_, err := Import(bytes.NewReader(buf.Bytes()), ImportOptions{Inbox: dst})
	if err == nil {
		t.Fatal("a file past the per-file limit should be refused")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("the refusal should name the limit: %v", err)
	}
}

// The directory component is checked; the file component was not. On
// Windows a backslash is a separator, so `..\..\evil.txt` is a traversal
// that path.Clean leaves entirely alone.
func TestImportRefusesUnsafeFileNames(t *testing.T) {
	man, _ := json.Marshal(Manifest{Format: Format, Version: Version})
	// ".." is in the archive too, but path.Clean folds `captures/ok/..`
	// into `captures` before anything here sees it, so it is never a file
	// of a capture and never reported as one — only never written.
	hostile := []string{`..\..\..\evil.txt`, `sub\evil.txt`, ".hidden"}
	inArchive := append([]string{".."}, hostile...)
	blob := buildArchive(t, func(tw *tar.Writer) {
		if err := writeFileEntry(tw, ManifestPath, man, zeroTime()); err != nil {
			t.Fatal(err)
		}
		if err := writeFileEntry(tw, "captures/ok/meta.json", []byte(`{"url":"https://x.test/"}`), zeroTime()); err != nil {
			t.Fatal(err)
		}
		for _, name := range inArchive {
			if err := writeFileEntry(tw, "captures/ok/"+name, []byte("pwned"), zeroTime()); err != nil {
				t.Fatal(err)
			}
		}
	})

	dst := filepath.Join(t.TempDir(), "inbox")
	res, err := Import(bytes.NewReader(blob), ImportOptions{Inbox: dst})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Imported) != 1 {
		t.Fatalf("the capture itself should still land: %+v", res)
	}
	// Whatever landed holds meta.json and nothing the archive smuggled in.
	files, err := os.ReadDir(res.Imported[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name() != capture.MetaFile {
			t.Errorf("unsafe file %q was unpacked", f.Name())
		}
	}
	if body, _ := os.ReadFile(filepath.Join(res.Imported[0].Path, capture.MetaFile)); !strings.Contains(string(body), "x.test") {
		t.Errorf("meta.json = %q", body)
	}
	// (".." is left out here: joined onto the capture it is the inbox
	// itself, which of course exists. That it wrote nothing is what the
	// directory listing above says.)
	for _, name := range hostile {
		if _, err := os.Lstat(filepath.Join(dst, "ok", name)); err == nil {
			t.Errorf("%q was written", name)
		}
	}
	reported := map[string]bool{}
	for _, s := range res.Skipped {
		reported[s.Name] = true
	}
	for _, name := range hostile {
		if !reported["captures/ok/"+name] {
			t.Errorf("%q was dropped without a word: %+v", name, res.Skipped)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// onStaging plants a directory in the inbox the moment the import has
// started staging a capture — which is to say, after the import has
// already decided that name was free. That is the window between the
// collision check and the rename, and the inbox is live in it.
type onStaging struct {
	r     io.Reader
	inbox string
	fired bool
	do    func()
}

func (o *onStaging) Read(p []byte) (int, error) {
	if !o.fired {
		entries, _ := os.ReadDir(o.inbox)
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), ".tmp-import-") {
				o.fired = true
				o.do()
				break
			}
		}
	}
	return o.r.Read(p)
}

// plant writes a capture directory straight into the inbox, as another
// process or a watcher would.
func plant(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, capture.MetaFile),
		[]byte(`{"url":"https://local.test/","title":"Mine"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A capture that appears in the inbox after the import checked the name
// must not be renamed over, and must not take the rest of the import down
// with it: skip means skip, however late the collision turns up.
func TestImportSkipsACaptureThatAppearsMidImport(t *testing.T) {
	src := seed(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	blob, _ := exportToBytes(t, ExportOptions{Inbox: src})
	entries, _ := capture.List(src)
	name := filepath.Base(entries[0].Path)

	dst := filepath.Join(t.TempDir(), "inbox")
	if err := os.MkdirAll(dst, 0o700); err != nil {
		t.Fatal(err)
	}
	hook := &onStaging{
		r: iotest.OneByteReader(bytes.NewReader(blob)), inbox: dst,
		do: func() { plant(t, filepath.Join(dst, name)) },
	}
	res, err := Import(hook, ImportOptions{Inbox: dst})
	if err != nil {
		t.Fatalf("a capture that appeared mid-import aborted the import: %v", err)
	}
	if !hook.fired {
		t.Fatal("nothing was planted; the test proves nothing")
	}
	if len(res.Imported) != 0 {
		t.Errorf("skip imported over the capture that appeared: %+v", res.Imported)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "already in the inbox") {
		t.Errorf("skipped = %+v", res.Skipped)
	}
	body, err := os.ReadFile(filepath.Join(dst, name, capture.MetaFile))
	if err != nil || !strings.Contains(string(body), "local.test") {
		t.Errorf("the capture that was already there was overwritten: %q %v", body, err)
	}
	left, _ := os.ReadDir(dst)
	if len(left) != 1 {
		names := make([]string, 0, len(left))
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("the inbox holds %v, want only the capture that was already there", names)
	}
}

// The same race under --on-collision rename: the name the import picked to
// rename to is taken while it is unpacking, so it has to pick another one
// rather than fail.
func TestImportRenamesPastANameTakenMidImport(t *testing.T) {
	src := seed(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	blob, _ := exportToBytes(t, ExportOptions{Inbox: src})
	entries, _ := capture.List(src)
	name := filepath.Base(entries[0].Path)

	dst := filepath.Join(t.TempDir(), "inbox")
	plant(t, filepath.Join(dst, name)) // the collision the import knows about
	hook := &onStaging{                //
		r: iotest.OneByteReader(bytes.NewReader(blob)), inbox: dst,
		do: func() { plant(t, filepath.Join(dst, name+"-2")) }, // and the one it does not
	}
	res, err := Import(hook, ImportOptions{Inbox: dst, OnCollision: CollisionRename})
	if err != nil {
		t.Fatalf("a name taken mid-import aborted the import: %v", err)
	}
	if !hook.fired {
		t.Fatal("nothing was planted; the test proves nothing")
	}
	if len(res.Imported) != 1 || !res.Imported[0].Renamed {
		t.Fatalf("imported %+v", res.Imported)
	}
	if got := filepath.Base(res.Imported[0].Path); got != name+"-3" {
		t.Errorf("landed as %s, want %s — the taken name must be stepped past", got, name+"-3")
	}
	for _, taken := range []string{name, name + "-2"} {
		body, _ := os.ReadFile(filepath.Join(dst, taken, capture.MetaFile))
		if !strings.Contains(string(body), "local.test") {
			t.Errorf("%s was overwritten: %q", taken, body)
		}
	}
}

func TestImportSkipsDerivedFiles(t *testing.T) {
	man, _ := json.Marshal(Manifest{Format: Format, Version: Version})
	const dir = "captures/2026-09-20T10-00-00Z-x/"
	blob := buildArchive(t, func(tw *tar.Writer) {
		for name, body := range map[string]string{
			ManifestPath:                string(man),
			dir + capture.MetaFile:      `{"url":"https://a.test/","title":"t","capturedAt":"2026-09-20T10:00:00Z"}`,
			dir + "readable.md":         "# kept",
			dir + "classification.json": `{"category":"planted"}`,
			dir + "summary.json":        `{"state":"done"}`,
			dir + "summary.md":          "planted",
		} {
			if err := writeFileEntry(tw, name, []byte(body), zeroTime()); err != nil {
				t.Fatal(err)
			}
		}
	})
	inbox := filepath.Join(t.TempDir(), "inbox")
	if _, err := Import(bytes.NewReader(blob), ImportOptions{Inbox: inbox}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	entries, _ := os.ReadDir(inbox)
	if len(entries) != 1 {
		t.Fatalf("want one capture, got %d", len(entries))
	}
	got := filepath.Join(inbox, entries[0].Name())
	for _, f := range []string{"classification.json", "summary.json", "summary.md"} {
		if _, err := os.Stat(filepath.Join(got, f)); err == nil {
			t.Errorf("%s came out of an archive; it is regenerated locally", f)
		}
	}
	if _, err := os.Stat(filepath.Join(got, "readable.md")); err != nil {
		t.Errorf("readable.md is capture content and must survive: %v", err)
	}
}

func TestImportBoundsInflatedIgnoredMembers(t *testing.T) {
	old := maxInflatedBytes
	maxInflatedBytes = 1 << 20
	defer func() { maxInflatedBytes = old }()
	man, _ := json.Marshal(Manifest{Format: Format, Version: Version})
	blob := buildArchive(t, func(tw *tar.Writer) {
		if err := writeFileEntry(tw, ManifestPath, man, zeroTime()); err != nil {
			t.Fatal(err)
		}
		if err := writeFileEntry(tw, "elsewhere/bomb.bin", make([]byte, 8<<20), zeroTime()); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := Import(bytes.NewReader(blob), ImportOptions{Inbox: filepath.Join(t.TempDir(), "inbox")}); err == nil {
		t.Fatal("an archive that inflates past the cap must be refused even when the bytes are out of scope")
	}
}

func TestCopyIntoRefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "readable.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip("no symlinks here")
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	note, err := copyInto(tw, "captures/x/readable.md", link, 7, zeroTime())
	if err != nil {
		t.Fatal(err)
	}
	if note == nil {
		t.Fatal("a symlink must be refused with a note")
	}
	tw.Close()
	if strings.Contains(buf.String(), "hunter2") {
		t.Fatal("the symlink target was archived")
	}
}
