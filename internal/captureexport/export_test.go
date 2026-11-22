package captureexport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
)

// seed writes captures into a fresh inbox and returns it.
func seed(t *testing.T, metas ...capture.Meta) string {
	t.Helper()
	inbox := filepath.Join(t.TempDir(), "inbox")
	for _, meta := range metas {
		w := &capture.Writer{Inbox: inbox}
		res, err := w.Write(&capture.Envelope{
			Meta: meta,
			Artifacts: map[string]capture.Artifact{
				capture.ArtifactMHTML:    capture.Inline([]byte("archive of " + meta.URL)),
				capture.ArtifactReadable: capture.Inline([]byte("# " + meta.Title + "\n\nbody\n")),
			},
		})
		if err != nil {
			t.Fatalf("seed %s: %v", meta.URL, err)
		}
		_ = res
	}
	return inbox
}

func strptr(s string) *string { return &s }

func exportToBytes(t *testing.T, opts ExportOptions) ([]byte, *Manifest) {
	t.Helper()
	var buf bytes.Buffer
	man, err := Export(&buf, opts)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return buf.Bytes(), man
}

// readArchive returns every member of the archive as path -> bytes.
func readArchive(t *testing.T, blob []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gz.Close()
	out := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = body
	}
	return out
}

func TestExportArchiveLayout(t *testing.T) {
	inbox := seed(t,
		capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"},
		capture.Meta{URL: "https://example.com/b", Title: "B", CapturedAt: "2026-09-21T10:00:00Z"},
	)
	blob, man := exportToBytes(t, ExportOptions{Inbox: inbox, Host: "testhost"})
	members := readArchive(t, blob)

	if _, ok := members[ManifestPath]; !ok {
		t.Fatalf("archive has no %s, members: %v", ManifestPath, keys(members))
	}
	if readme, ok := members[ReadmePath]; !ok || !strings.Contains(string(readme), "tar -xzf") {
		t.Errorf("archive should carry a README explaining how to read it without the tool")
	}
	if man.Count != 2 || len(man.Captures) != 2 {
		t.Fatalf("manifest counted %d captures", man.Count)
	}
	if man.Format != Format || man.Version != Version {
		t.Errorf("manifest = %s v%d", man.Format, man.Version)
	}
	if man.Source.Inbox != inbox || man.Source.Host != "testhost" {
		t.Errorf("manifest source = %+v", man.Source)
	}

	for _, row := range man.Captures {
		if !strings.HasPrefix(row.Dir, CapturesDir+"/") {
			t.Errorf("capture dir %q should sit under %s/", row.Dir, CapturesDir)
		}
		names := make([]string, 0, len(row.Files))
		for _, f := range row.Files {
			names = append(names, f.Name)
			body, ok := members[path.Join(row.Dir, f.Name)]
			if !ok {
				t.Fatalf("manifest lists %s/%s but the archive does not hold it", row.Dir, f.Name)
			}
			if int64(len(body)) != f.Size {
				t.Errorf("%s: manifest size %d, archive holds %d", f.Name, f.Size, len(body))
			}
			sum := sha256.Sum256(body)
			if hex.EncodeToString(sum[:]) != f.SHA256 {
				t.Errorf("%s: manifest checksum does not match the bytes", f.Name)
			}
		}
		sort.Strings(names)
		want := []string{capture.MetaFile, capture.ArtifactMHTML, capture.ArtifactReadable}
		sort.Strings(want)
		if strings.Join(names, ",") != strings.Join(want, ",") {
			t.Errorf("capture holds %v, want %v", names, want)
		}
	}
}

func TestExportManifestIsReadableOnItsOwn(t *testing.T) {
	inbox := seed(t, capture.Meta{
		URL: "https://example.com/a", CanonicalURL: "https://example.com/a",
		Title: "A", CapturedAt: "2026-09-20T10:00:00Z",
		Collection: strptr("research"), Tags: []string{"go", "web"}, Source: "crawl",
	})
	blob, _ := exportToBytes(t, ExportOptions{Inbox: inbox})
	var man Manifest
	if err := json.Unmarshal(readArchive(t, blob)[ManifestPath], &man); err != nil {
		t.Fatalf("manifest.json does not parse on its own: %v", err)
	}
	row := man.Captures[0]
	if row.Collection != "research" || row.CaptureSrc != "crawl" || strings.Join(row.Tags, ",") != "go,web" {
		t.Errorf("manifest row lost provenance: %+v", row)
	}
	if row.ContentHash == "" || row.CapturedAt == "" || row.Title != "A" {
		t.Errorf("manifest row = %+v", row)
	}
}

func TestExportFilters(t *testing.T) {
	inbox := seed(t,
		capture.Meta{URL: "https://example.com/old", Title: "Old", CapturedAt: "2026-09-01T10:00:00Z"},
		capture.Meta{URL: "https://example.com/new", Title: "New", CapturedAt: "2026-09-20T10:00:00Z", Collection: strptr("research")},
		capture.Meta{URL: "https://example.com/other", Title: "Other", CapturedAt: "2026-09-21T10:00:00Z", Collection: strptr("misc")},
	)

	since, err := ParseSince("2026-09-15")
	if err != nil {
		t.Fatalf("ParseSince: %v", err)
	}
	_, man := exportToBytes(t, ExportOptions{Inbox: inbox, Since: since, SinceText: "2026-09-15"})
	if man.Count != 2 {
		t.Errorf("--since kept %d captures, want 2", man.Count)
	}
	if man.Filters == nil || man.Filters.Since != "2026-09-15" {
		t.Errorf("the manifest should record the filter, got %+v", man.Filters)
	}

	_, man = exportToBytes(t, ExportOptions{Inbox: inbox, Collection: "Research"})
	if man.Count != 1 || man.Captures[0].Title != "New" {
		t.Errorf("--collection kept %d captures: %+v", man.Count, man.Captures)
	}

	_, man = exportToBytes(t, ExportOptions{Inbox: inbox, Since: since, Collection: "misc"})
	if man.Count != 1 || man.Captures[0].Title != "Other" {
		t.Errorf("combined filters kept %+v", man.Captures)
	}
}

func TestExportEmptyInbox(t *testing.T) {
	_, man := exportToBytes(t, ExportOptions{Inbox: filepath.Join(t.TempDir(), "nothing")})
	if man.Count != 0 || len(man.Captures) != 0 {
		t.Errorf("an empty inbox should produce an empty archive, got %+v", man)
	}
}

func TestParseSince(t *testing.T) {
	if got, err := ParseSince(""); err != nil || !got.IsZero() {
		t.Errorf("empty --since should mean no filter, got %v %v", got, err)
	}
	if _, err := ParseSince("last tuesday"); err == nil {
		t.Error("an unparseable date should be refused")
	}
	got, err := ParseSince("2026-09-15T08:30:00Z")
	if err != nil || !got.Equal(time.Date(2026, 9, 15, 8, 30, 0, 0, time.UTC)) {
		t.Errorf("ParseSince(RFC3339) = %v, %v", got, err)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var _ = os.ReadDir

// bigSeed writes one capture whose artifact is large and incompressible, so
// that copying it forces gzip to push bytes at the writer underneath —
// which is what lets a test act while the export is mid-stream.
func bigSeed(t *testing.T, inbox string, meta capture.Meta, size int) string {
	t.Helper()
	body := make([]byte, size)
	if _, err := rand.Read(body); err != nil {
		t.Fatal(err)
	}
	w := &capture.Writer{Inbox: inbox}
	res, err := w.Write(&capture.Envelope{
		Meta: meta,
		Artifacts: map[string]capture.Artifact{
			capture.ArtifactMHTML:    capture.Inline(body),
			capture.ArtifactReadable: capture.Inline([]byte("# " + meta.Title + "\n")),
		},
	})
	if err != nil {
		t.Fatalf("seed %s: %v", meta.URL, err)
	}
	return res.Path
}

// onFirstWrite runs once, when the export first pushes bytes.
type onFirstWrite struct {
	w     io.Writer
	fired bool
	do    func()
}

func (o *onFirstWrite) Write(p []byte) (int, error) {
	if !o.fired {
		o.fired = true
		o.do()
	}
	return o.w.Write(p)
}

// The inbox is a live drop-zone: a watcher ingests from it and captures are
// deleted from it while an export is running. One capture disappearing must
// not throw away the whole archive — including the captures already
// streamed into it.
func TestExportSkipsACaptureThatVanishesMidStream(t *testing.T) {
	inbox := filepath.Join(t.TempDir(), "inbox")
	bigSeed(t, inbox, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"}, 1<<20)
	doomed := bigSeed(t, inbox, capture.Meta{URL: "https://example.com/z", Title: "Z", CapturedAt: "2026-09-21T10:00:00Z"}, 4096)

	var buf bytes.Buffer
	sink := &onFirstWrite{w: &buf, do: func() {
		if err := os.RemoveAll(doomed); err != nil {
			t.Errorf("remove %s: %v", doomed, err)
		}
	}}
	man, err := Export(sink, ExportOptions{Inbox: inbox})
	if err != nil {
		t.Fatalf("one deleted capture aborted the whole export: %v", err)
	}
	if !sink.fired {
		t.Fatal("the capture was never deleted mid-stream; the test proves nothing")
	}

	members := readArchive(t, buf.Bytes())
	if _, ok := members["captures/"+filepath.Base(doomed)+"/"+capture.ArtifactMHTML]; ok {
		t.Error("the archive holds a file that was deleted before it was read")
	}
	// The capture that was still there is in the archive, whole.
	for _, row := range man.Captures {
		if strings.HasSuffix(row.Dir, filepath.Base(doomed)) {
			continue
		}
		for _, f := range row.Files {
			body, ok := members[path.Join(row.Dir, f.Name)]
			if !ok {
				t.Fatalf("%s/%s was dropped along with the deleted capture", row.Dir, f.Name)
			}
			if int64(len(body)) != f.Size {
				t.Errorf("%s: %d bytes, manifest says %d", f.Name, len(body), f.Size)
			}
		}
	}
	if !noted(man.Skipped, filepath.Base(doomed)) {
		t.Errorf("the capture that vanished should be reported, got %+v", man.Skipped)
	}
	// And the archive is still an archive: it imports.
	dst := filepath.Join(t.TempDir(), "restored")
	res, err := Import(bytes.NewReader(buf.Bytes()), ImportOptions{Inbox: dst})
	if err != nil {
		t.Fatalf("the archive written around the deletion does not import: %v", err)
	}
	if len(res.Imported) != 1 || res.Imported[0].Title != "A" {
		t.Errorf("restored %+v", res.Imported)
	}
}

// The same for one artifact rather than a whole capture: the rest of the
// envelope is worth keeping, and the archive says what is missing from it.
func TestExportSkipsAnArtifactItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything; this test needs a file it cannot open")
	}
	inbox := seed(t,
		capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"},
		capture.Meta{URL: "https://example.com/b", Title: "B", CapturedAt: "2026-09-21T10:00:00Z"},
	)
	entries, err := capture.List(inbox)
	if err != nil || len(entries) != 2 {
		t.Fatalf("seed: %v %v", entries, err)
	}
	locked := filepath.Join(entries[0].Path, capture.ArtifactMHTML)
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o600) })

	blob, man := exportToBytes(t, ExportOptions{Inbox: inbox})
	if man.Count != 2 {
		t.Fatalf("an unreadable file cost %d captures", 2-man.Count)
	}
	if !noted(man.Skipped, capture.ArtifactMHTML) {
		t.Errorf("the unreadable artifact should be reported, got %+v", man.Skipped)
	}
	members := readArchive(t, blob)
	for _, row := range man.Captures {
		for _, f := range row.Files {
			if _, ok := members[path.Join(row.Dir, f.Name)]; !ok {
				t.Errorf("the manifest lists %s/%s but the archive does not hold it", row.Dir, f.Name)
			}
		}
	}
	// Both captures still land, and the readable half of the damaged one
	// comes with it.
	dst := filepath.Join(t.TempDir(), "restored")
	res, err := Import(bytes.NewReader(blob), ImportOptions{Inbox: dst})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(res.Imported) != 2 {
		t.Fatalf("restored %+v", res.Imported)
	}
}

// noted reports whether any note mentions name.
func noted(notes []Note, name string) bool {
	for _, n := range notes {
		if strings.Contains(n.Name, name) {
			return true
		}
	}
	return false
}

func TestExportSkipsRecordingCaptures(t *testing.T) {
	inbox := seed(t,
		capture.Meta{URL: "https://example.com/page", Title: "Page", CapturedAt: "2026-09-01T10:00:00Z"},
		capture.Meta{URL: "https://example.com/rec", Title: "Rec", CapturedAt: "2026-09-02T10:00:00Z", Source: capture.SourceRecording},
	)
	_, man := exportToBytes(t, ExportOptions{Inbox: inbox})
	if man.Count != 1 || man.Captures[0].Title != "Page" {
		t.Fatalf("a recording must not be exported by default: %+v", man.Captures)
	}
	noted := false
	for _, n := range man.Skipped {
		if strings.Contains(n.Reason, "recording") {
			noted = true
		}
	}
	if !noted {
		t.Errorf("the skipped recording should be noted: %+v", man.Skipped)
	}
	_, man = exportToBytes(t, ExportOptions{Inbox: inbox, IncludeRecordings: true})
	if man.Count != 2 {
		t.Errorf("IncludeRecordings kept %d, want 2", man.Count)
	}
}
