package captureexport

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
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

// Collision says what to do when a capture in the archive has the same
// directory name as one already in the inbox.
type Collision string

const (
	// CollisionSkip leaves the existing capture alone. It is the default:
	// an import is additive, and a re-import of an overlapping archive
	// should be a no-op rather than a rewrite.
	CollisionSkip Collision = "skip"
	// CollisionRename keeps both, giving the incoming one a suffix.
	CollisionRename Collision = "rename"
	// CollisionOverwrite replaces the existing capture.
	CollisionOverwrite Collision = "overwrite"
)

// ParseCollision validates the --on-collision value.
func ParseCollision(s string) (Collision, error) {
	switch Collision(strings.ToLower(strings.TrimSpace(s))) {
	case "", CollisionSkip:
		return CollisionSkip, nil
	case CollisionRename:
		return CollisionRename, nil
	case CollisionOverwrite:
		return CollisionOverwrite, nil
	}
	return "", fmt.Errorf("captureexport: unknown collision mode %q; want skip, rename or overwrite", s)
}

// Import limits.
//
// An archive read here arrived from somewhere else by definition, and it
// unpacks into a home directory. Every dimension it can grow in is
// bounded, and the bounds are set by what a real capture library looks
// like rather than by what a disk can hold:
//
//   - a capture is allowed to be large — an MHTML of a heavy page, or a
//     print-to-PDF, genuinely is — but the biggest ones run to tens of
//     megabytes, so 256 MiB is already an order of magnitude of headroom;
//   - 4 GiB is a whole library, and it fits on a laptop with room left
//     over. Moving more than that is several archives, which is also how
//     it wants to be resumed when one of them fails halfway;
//   - the counts matter more than the bytes: a million one-byte captures
//     weigh nothing and cost an inode each, and an inbox that gained
//     100,000 directories from one command is not an import anybody asked
//     for. maxEntries bounds the loop itself, ahead of all of it.
const (
	maxFileBytes       int64 = 256 << 20 // one artifact
	maxTotalBytes      int64 = 4 << 30   // one archive
	maxCaptures              = 20000     // capture directories in one archive
	maxFilesPerCapture       = 64        // files in one capture directory
	maxEntries               = 400000    // tar members, counted before anything else
	maxMetaBytes       int64 = 4 << 20   // meta.json, which a dry run holds in memory
	maxNotes                 = 1000      // skips reported before they are only noise
)

// maxInflatedBytes bounds what the gzip layer may produce in all, members
// this importer ignores included: tar.Next inflates and discards them, and
// a bomb of out-of-scope members would otherwise cost unbounded time.
var maxInflatedBytes = maxTotalBytes + 64<<20

// derivedFiles are written by this machine's own classifier and summarizer
// and later trusted as the user's state (a review decision, a finished
// summary). An archive does not get to plant them; they are regenerated.
var derivedFiles = map[string]bool{
	"classification.json": true, // captureclassify.FileName
	"summary.json":        true, // capturesummary.StatusFile
	"summary.md":          true, // capturesummary.SummaryFile
}

// errInflated is returned by cappedReader past maxInflatedBytes.
var errInflated = errors.New("captureexport: archive inflates past its size limit")

// cappedReader fails once more than max bytes have been read through it.
type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, errInflated
	}
	return n, err
}

// ImportOptions configures a restore.
type ImportOptions struct {
	// Inbox is where captures are restored to; empty means the default.
	Inbox string
	// OnCollision decides what happens to a name that already exists.
	OnCollision Collision
	// DryRun reports what would happen and writes nothing — no staging
	// directory, no artifact, not one byte.
	DryRun bool
	// Profile is the profile the imported captures belong to; "" leaves them
	// unprofiled. The archive's own profile is never trusted (R11-12).
	Profile string
}

// Imported is one capture that landed (or would have).
type Imported struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	URL      string `json:"url,omitempty"`
	Title    string `json:"title,omitempty"`
	Bytes    int64  `json:"bytes"`
	Renamed  bool   `json:"renamed,omitempty"`
	Replaced bool   `json:"replaced,omitempty"`
}

// ImportResult is what an import did.
type ImportResult struct {
	Inbox    string     `json:"inbox"`
	Manifest *Manifest  `json:"manifest,omitempty"`
	Imported []Imported `json:"imported"`
	Skipped  []Note     `json:"skipped,omitempty"`
	DryRun   bool       `json:"dryRun,omitempty"`
}

// Import restores the captures in a gzipped tar archive into the inbox.
//
// One capture is unpacked at a time: its files go into a staging directory
// inside the inbox, and the moment the archive moves on to the next
// capture that directory is renamed into place. That is the same
// discipline capture.Writer uses — nothing watching the inbox ever sees
// half a capture — and it also means an archive of a million tiny captures
// costs one staging directory rather than a million. An import that fails
// halfway leaves the captures it already published; each of them is whole.
//
// A capture whose files are not contiguous in the archive is skipped with
// a note rather than merged: an export never writes one that way, and
// guessing what a scattered directory was supposed to be is how a capture
// ends up holding someone else's artifact.
func Import(r io.Reader, opts ImportOptions) (*ImportResult, error) {
	inbox := opts.Inbox
	if strings.TrimSpace(inbox) == "" {
		inbox = capture.DefaultInbox()
	}
	mode := opts.OnCollision
	if mode == "" {
		mode = CollisionSkip
	}
	if !opts.DryRun {
		if err := os.MkdirAll(inbox, 0o700); err != nil {
			return nil, fmt.Errorf("create inbox %s: %w", inbox, err)
		}
	}

	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	defer gz.Close()

	res := &ImportResult{Inbox: inbox, Imported: []Imported{}, DryRun: opts.DryRun}
	imp := &importer{opts: opts, inbox: inbox, mode: mode, res: res, seen: map[string]bool{}}
	defer imp.discard()

	tr := tar.NewReader(&cappedReader{r: gz, left: maxInflatedBytes})
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if err := imp.entry(hdr, tr); err != nil {
			return nil, err
		}
	}
	if !imp.seenManifest && imp.captures == 0 {
		return nil, errors.New("captureexport: this is not a capture archive — no manifest.json and no captures/")
	}
	if err := imp.finish(); err != nil {
		return nil, err
	}
	sort.Slice(res.Imported, func(i, j int) bool { return res.Imported[i].Name < res.Imported[j].Name })
	return res, nil
}

// importer holds the state of one import: the capture currently being
// unpacked, and the counters that bound what the archive can ask for.
type importer struct {
	opts         ImportOptions
	inbox        string
	mode         Collision
	res          *ImportResult
	cur          *staging        // the capture being unpacked, if any
	seen         map[string]bool // capture names already started
	captures     int
	entries      int
	total        int64
	seenManifest bool
}

// staging is the one capture being unpacked. dir is its staging directory,
// empty for a capture being skipped and for every capture in a dry run,
// which reads meta.json into memory instead of writing anything.
type staging struct {
	name  string
	dir   string
	files int
	bytes int64
	meta  []byte
	skip  bool
}

// entry handles one tar member.
func (im *importer) entry(hdr *tar.Header, tr io.Reader) error {
	im.entries++
	if im.entries > maxEntries {
		return fmt.Errorf("captureexport: archive holds more than %d entries", maxEntries)
	}
	name := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeDir:
	default:
		// A capture is files in a flat directory. A symlink, a device node
		// or a hard link in this archive is either corruption or an
		// attempt to write outside the inbox; neither gets unpacked.
		return nil
	}
	if name == ManifestPath {
		return im.readManifest(tr)
	}
	if name == ReadmePath {
		return nil
	}
	dir, file, ok := splitCapturePath(name)
	if !ok {
		return nil // anything outside captures/<name>/<file> is not ours
	}
	if hdr.Typeflag == tar.TypeDir || file == "" {
		return nil // the directory entry itself; the files create it
	}
	if hdr.Size > maxFileBytes {
		return fmt.Errorf("captureexport: %s is %d bytes, past the %d byte limit", name, hdr.Size, maxFileBytes)
	}
	im.total += hdr.Size
	if im.total > maxTotalBytes {
		return fmt.Errorf("captureexport: archive is larger than the %d byte limit", maxTotalBytes)
	}

	cur, err := im.capture(dir)
	if err != nil {
		return err
	}
	if cur.skip {
		return drain(tr)
	}
	cur.files++
	if cur.files > maxFilesPerCapture {
		return fmt.Errorf("captureexport: %s holds more than %d files", dir, maxFilesPerCapture)
	}
	// The directory name is checked in capture(); the file name is checked
	// here, and by the same rule the inbox itself uses. path.Clean folds a
	// '/' traversal away long before this, but it leaves '..\..\x' alone —
	// one component on this machine, three on Windows.
	if file != capture.MetaFile && !capture.ValidArtifactName(file) {
		im.skip(name, "unsafe file name")
		return drain(tr)
	}
	if derivedFiles[file] {
		im.skip(name, "derived file; regenerated locally")
		return drain(tr)
	}
	cur.bytes += hdr.Size
	if im.opts.DryRun {
		return cur.inspect(file, tr)
	}
	return writeStagedFile(filepath.Join(cur.dir, file), tr, hdr.Size)
}

// capture returns the capture the next file belongs to, publishing the
// previous one first when the archive has moved on.
func (im *importer) capture(name string) (*staging, error) {
	if im.cur != nil && im.cur.name == name {
		return im.cur, nil
	}
	if err := im.finish(); err != nil {
		return nil, err
	}

	cur := &staging{name: name}
	im.cur = cur
	if im.seen[name] {
		cur.skip = true
		im.skip(name, "split across the archive")
		return cur, nil
	}
	im.seen[name] = true
	im.captures++
	if im.captures > maxCaptures {
		return nil, fmt.Errorf("captureexport: archive holds more than %d captures", maxCaptures)
	}
	if !validCaptureName(name) {
		cur.skip = true
		im.skip(name, "unsafe directory name")
		return cur, nil
	}
	if _, err := os.Lstat(filepath.Join(im.inbox, name)); err == nil && im.mode == CollisionSkip {
		cur.skip = true
		im.skip(name, "already in the inbox")
		return cur, nil
	}
	if im.opts.DryRun {
		return cur, nil // nothing is created, so there is nothing to stage
	}
	dir, err := os.MkdirTemp(im.inbox, ".tmp-import-*")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure staging dir: %w", err)
	}
	cur.dir = dir
	return cur, nil
}

// finish publishes the capture that has just been unpacked. A capture
// without a meta.json is dropped here rather than published: the inbox
// contract is that a directory with a meta.json is a landed capture.
func (im *importer) finish() error {
	cur := im.cur
	im.cur = nil
	if cur == nil || cur.skip {
		return nil
	}
	meta, err := cur.readMeta()
	if err != nil {
		im.drop(cur, "no readable "+capture.MetaFile)
		return nil
	}
	if cur.dir != "" {
		if err := im.adoptMeta(cur.dir, meta); err != nil {
			return err
		}
	}
	total := cur.bytes
	if cur.dir != "" {
		if total, err = dirBytes(cur.dir); err != nil {
			return err
		}
	}
	// The destination is resolved here rather than when the capture
	// started, because the inbox is live: a watcher, another import or the
	// extension can have put that name there in between. A collision
	// decided at check time and acted on at rename time is how skip turns
	// into "publish %s: file exists" — an aborted import, after other
	// captures have already landed.
	for attempt := 0; ; attempt++ {
		dest, renamed, exists, err := im.destination(cur.name)
		if err != nil {
			return err
		}
		if exists && im.mode == CollisionSkip {
			im.drop(cur, "already in the inbox")
			return nil
		}
		entry := Imported{
			Name: filepath.Base(dest), Path: dest, URL: meta.DedupeURL(),
			Title: meta.Title, Bytes: total, Renamed: renamed,
		}
		if im.opts.DryRun {
			im.res.Imported = append(im.res.Imported, entry)
			return nil
		}
		// An overwrite never has a moment with neither copy: the old one is
		// renamed aside, the new one published, and only then the old one
		// deleted (R11-11).
		oldAside := ""
		if exists && im.mode == CollisionOverwrite && !renamed {
			oldAside = filepath.Join(im.inbox, fmt.Sprintf(".tmp-old-%s-%d", filepath.Base(dest), time.Now().UnixNano()))
			if err := os.Rename(dest, oldAside); err != nil {
				return fmt.Errorf("replace %s: %w", dest, err)
			}
			entry.Replaced = true
		}
		if err := os.Rename(cur.dir, dest); err != nil {
			if oldAside != "" {
				_ = os.Rename(oldAside, dest) // put the original back
			}
			// Something took the name in the last instant. Ask again —
			// the answer is different now — rather than fail an import
			// that has nothing wrong with it.
			if attempt < publishAttempts {
				if _, statErr := os.Lstat(dest); statErr == nil {
					continue
				}
			}
			return fmt.Errorf("publish %s: %w", dest, err)
		}
		if oldAside != "" {
			beforeOldRemoved(oldAside)
			if err := os.RemoveAll(oldAside); err != nil {
				return fmt.Errorf("remove the replaced %s: %w", oldAside, err)
			}
		}
		im.res.Imported = append(im.res.Imported, entry)
		return nil
	}
}

// beforeOldRemoved runs between publishing an overwrite and deleting the
// copy it replaced; tests use it to look at that moment.
var beforeOldRemoved = func(oldPath string) {}

// publishAttempts bounds the re-resolve loop above. Losing the same name
// three times running is not a race any more, it is a fight.
const publishAttempts = 3

func (im *importer) readManifest(tr io.Reader) error {
	im.seenManifest = true
	blob, err := io.ReadAll(io.LimitReader(tr, 64<<20))
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	var man Manifest
	if err := json.Unmarshal(blob, &man); err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	if man.Format != Format {
		return fmt.Errorf("captureexport: %q is not a capture archive (format %q)", ManifestPath, man.Format)
	}
	if man.Version > Version {
		return fmt.Errorf("captureexport: archive version %d is newer than this build understands (%d)", man.Version, Version)
	}
	im.res.Manifest = &man
	return nil
}

// destination resolves the final directory for a capture, stepping past an
// existing name when renaming. exists reports whether the capture's own
// name is taken, which is what the collision modes turn on — and which is
// only true of this instant, never of the check that started the capture.
func (im *importer) destination(name string) (dest string, renamed, exists bool, err error) {
	dest = filepath.Join(im.inbox, name)
	if _, err := os.Lstat(dest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return dest, false, false, nil
		}
		return "", false, false, fmt.Errorf("check %s: %w", dest, err)
	}
	if im.mode != CollisionRename {
		return dest, false, true, nil
	}
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", dest, n)
		if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, true, true, nil
		}
	}
	return "", false, true, fmt.Errorf("captureexport: no free name for %s", name)
}

// skip records why something in the archive was not taken. The list is
// bounded: an archive can hold more junk than a person will ever read.
func (im *importer) skip(name, reason string) {
	switch n := len(im.res.Skipped); {
	case n < maxNotes:
		im.res.Skipped = append(im.res.Skipped, Note{Name: name, Reason: reason})
	case n == maxNotes:
		im.res.Skipped = append(im.res.Skipped, Note{
			Name:   "…",
			Reason: fmt.Sprintf("more than %d entries were skipped; the rest are not listed", maxNotes),
		})
	}
}

// drop removes a staged capture that will not be published.
func (im *importer) drop(cur *staging, reason string) {
	if cur.dir != "" {
		_ = os.RemoveAll(cur.dir)
	}
	im.skip(cur.name, reason)
}

// discard removes the staging directory of a capture that never finished —
// a failed import, or an archive that ended mid-capture.
func (im *importer) discard() {
	if im.cur != nil && im.cur.dir != "" {
		_ = os.RemoveAll(im.cur.dir)
	}
}
