package captureexport

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
)

// ExportOptions selects what goes into an archive.
type ExportOptions struct {
	// Inbox is the directory to export from; empty means the default.
	Inbox string
	// Since keeps only captures taken at or after this instant. Zero keeps
	// everything.
	Since time.Time
	// SinceText is what the user typed, recorded in the manifest.
	SinceText string
	// Collection keeps only captures filed under this collection.
	Collection string
	// Only, when non-empty, keeps only these envelope directory names.
	Only []string
	// Now supplies the archive's timestamp; nil means time.Now.
	Now func() time.Time
	// Host is recorded as provenance; empty asks the OS.
	Host string
	// IncludeRecordings also exports browser recordings, which hold typed
	// values and DOM. Off by default.
	IncludeRecordings bool
}

// errBadDate is the parse failure for --since.
func errBadDate(s string) error {
	return fmt.Errorf("captureexport: %q is not a date — use 2026-09-21 or a full RFC3339 timestamp", s)
}

// ParseSince exposes the date parsing to callers so a bad --since fails at
// the flag rather than halfway through an export.
func ParseSince(s string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	return parseSince(strings.TrimSpace(s))
}

// Export writes a gzipped tar of the selected captures to w and returns the
// manifest it wrote.
//
// It reads each capture twice — once to checksum, once to copy — so that
// the manifest can lead the archive without any capture being held in
// memory. An export of a 2GB inbox costs one buffer, not 2GB.
//
// Those two passes run over a live directory: the inbox is a drop-zone, a
// watcher ingests from it, and captures are deleted out from under a
// reader as a matter of course. So a capture or an artifact that is gone
// by the time it is read is left out with a note (Manifest.Skipped, and
// skipped.json at the end of the archive) rather than taken as a reason to
// throw away an archive that is already half written.
func Export(w io.Writer, opts ExportOptions) (*Manifest, error) {
	inbox := opts.Inbox
	if strings.TrimSpace(inbox) == "" {
		inbox = capture.DefaultInbox()
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	entries, err := capture.List(inbox)
	if err != nil {
		return nil, err
	}

	only := map[string]bool{}
	for _, name := range opts.Only {
		only[filepath.Base(strings.TrimRight(name, string(filepath.Separator)))] = true
	}

	rows := make([]ManifestRow, 0, len(entries))
	var notes []Note
	var total int64
	for _, e := range entries {
		name := filepath.Base(e.Path)
		if len(only) > 0 && !only[name] {
			continue
		}
		// capture.List already read and decoded meta.json to build the
		// entry; reading it again here would parse every capture twice.
		meta := &e.Meta
		if meta.Source == capture.SourceRecording && !opts.IncludeRecordings {
			notes = append(notes, Note{Name: name, Reason: "not taken: recording (holds typed values and page DOM)"})
			continue
		}
		if !opts.Since.IsZero() && !capturedAtLeast(meta, opts.Since) {
			continue
		}
		if opts.Collection != "" && !strings.EqualFold(collectionOf(meta), opts.Collection) {
			continue
		}
		row, skipped, err := describe(e.Path, name, meta)
		if err != nil {
			// Listed a moment ago, unreadable now: one fewer capture, not
			// a failed export. Noted before the manifest is written, so
			// the manifest does not claim it.
			notes = append(notes, Note{Name: name, Reason: "not taken: " + err.Error()})
			continue
		}
		notes = append(notes, skipped...)
		rows = append(rows, row)
		total += row.Bytes
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Dir < rows[j].Dir })

	host := opts.Host
	if host == "" {
		host, _ = os.Hostname()
	}
	man := &Manifest{
		Format:    Format,
		Version:   Version,
		CreatedAt: now().UTC().Format(time.RFC3339),
		Tool:      "monoagentcli",
		Source:    Source{Inbox: inbox, Host: host},
		Count:     len(rows),
		Bytes:     total,
		Captures:  rows,
	}
	if opts.SinceText != "" || opts.Collection != "" {
		man.Filters = &Filters{Since: opts.SinceText, Collection: opts.Collection}
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	manBlob, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	manBlob = append(manBlob, '\n')
	modTime := now().UTC()
	if err := writeFileEntry(tw, ManifestPath, manBlob, modTime); err != nil {
		return nil, err
	}
	if err := writeFileEntry(tw, ReadmePath, []byte(readmeText), modTime); err != nil {
		return nil, err
	}
	for _, row := range rows {
		src := filepath.Join(inbox, filepath.Base(row.Dir))
		if err := writeDirEntry(tw, row.Dir, modTime); err != nil {
			return nil, err
		}
		for _, f := range row.Files {
			note, err := copyInto(tw, path.Join(row.Dir, f.Name), filepath.Join(src, f.Name), f.Size, modTime)
			if err != nil {
				return nil, err
			}
			if note != nil {
				notes = append(notes, *note)
			}
		}
	}
	// What was left out goes in last, because it is not known until
	// everything has been tried. The manifest at the front was written
	// before any of this, so a capture noted here is listed there and
	// absent from the archive — which is what this file is for.
	man.Skipped = notes
	if len(notes) > 0 {
		blob, err := json.MarshalIndent(map[string]any{"skipped": notes}, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encode %s: %w", SkippedPath, err)
		}
		if err := writeFileEntry(tw, SkippedPath, append(blob, '\n'), modTime); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("finish archive: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("finish archive: %w", err)
	}
	return man, nil
}

// describe checksums one capture directory without holding it in memory.
// A file that cannot be read is left out of the row with a note; only the
// directory itself being unreadable is an error, and that means the whole
// capture is gone.
func describe(dir, name string, meta *capture.Meta) (ManifestRow, []Note, error) {
	row := ManifestRow{
		Dir:          path.Join(CapturesDir, name),
		URL:          meta.URL,
		CanonicalURL: meta.CanonicalURL,
		Title:        meta.Title,
		CapturedAt:   meta.CapturedAt,
		ContentHash:  meta.ContentHash,
		Collection:   collectionOf(meta),
		Tags:         meta.Tags,
		CaptureSrc:   meta.Source,
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return row, nil, err
	}
	var notes []Note
	for _, f := range files {
		if f.IsDir() || !f.Type().IsRegular() {
			continue // an envelope is a flat directory of plain files
		}
		archived := path.Join(row.Dir, f.Name())
		info, err := f.Info()
		if err != nil {
			notes = append(notes, Note{Name: archived, Reason: "not taken: " + err.Error()})
			continue
		}
		sum, err := hashFile(filepath.Join(dir, f.Name()))
		if err != nil {
			notes = append(notes, Note{Name: archived, Reason: "not taken: " + err.Error()})
			continue
		}
		row.Files = append(row.Files, FileRow{Name: f.Name(), Size: info.Size(), SHA256: sum})
		row.Bytes += info.Size()
	}
	sort.Slice(row.Files, func(i, j int) bool { return row.Files[i].Name < row.Files[j].Name })
	return row, notes, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", filepath.Base(path), err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeFileEntry(tw *tar.Writer, name string, body []byte, modTime time.Time) error {
	hdr := &tar.Header{
		Typeflag: tar.TypeReg, Name: name, Size: int64(len(body)),
		Mode: 0o600, ModTime: modTime, Format: tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	_, err := tw.Write(body)
	return err
}

func writeDirEntry(tw *tar.Writer, name string, modTime time.Time) error {
	return tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o700,
		ModTime: modTime, Format: tar.FormatPAX,
	})
}

// copyInto streams one file into the archive, returning a note when it
// could not take it faithfully and an error only when the archive itself
// can no longer be written.
//
// The size in the manifest came from an earlier stat, so the header is
// written from a fresh one instead: between the two, the inbox is live.
// A file that vanishes is skipped whole; one that shrinks after its header
// is written cannot be — tar has been promised those bytes — so the entry
// is padded and the note says the checksum will not match.
func copyInto(tw *tar.Writer, name, src string, want int64, modTime time.Time) (*Note, error) {
	f, err := os.Open(src)
	if err != nil {
		return &Note{Name: name, Reason: "not taken: " + err.Error()}, nil
	}
	defer f.Close()

	size := want
	var note *Note
	if info, err := f.Stat(); err == nil && info.Size() != want {
		size = info.Size()
		note = &Note{Name: name, Reason: fmt.Sprintf(
			"changed while exporting: %d bytes, the manifest says %d", size, want)}
	}
	hdr := &tar.Header{
		Typeflag: tar.TypeReg, Name: name, Size: size,
		Mode: 0o600, ModTime: modTime, Format: tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return nil, fmt.Errorf("write %s: %w", name, err)
	}
	n, err := io.Copy(tw, io.LimitReader(f, size))
	if err != nil {
		return nil, fmt.Errorf("copy %s: %w", src, err)
	}
	if n < size {
		if err := padZeros(tw, size-n); err != nil {
			return nil, fmt.Errorf("write %s: %w", name, err)
		}
		note = &Note{Name: name, Reason: fmt.Sprintf(
			"truncated while exporting: %d of %d bytes, the rest is padding and the sha256 will not match", n, size)}
	}
	return note, nil
}

// padZeros finishes a tar entry whose file shrank after the header
// promised its length.
func padZeros(w io.Writer, n int64) error {
	var zeros [32 << 10]byte
	for n > 0 {
		chunk := int64(len(zeros))
		if chunk > n {
			chunk = n
		}
		if _, err := w.Write(zeros[:chunk]); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

func collectionOf(meta *capture.Meta) string {
	if meta.Collection == nil {
		return ""
	}
	return strings.TrimSpace(*meta.Collection)
}

// capturedAtLeast reports whether the capture happened at or after since. A
// capture whose timestamp will not parse is kept: dropping a document
// because its clock was odd is the worse failure.
func capturedAtLeast(meta *capture.Meta, since time.Time) bool {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, meta.CapturedAt); err == nil {
			return !t.Before(since)
		}
	}
	return true
}
