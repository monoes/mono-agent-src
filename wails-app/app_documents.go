// wails-app/app_documents.go
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/docscan"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ProfileDocument mirrors internal/vault.DocumentEntry's fields (that
// struct has no json tags -> PascalCase on the wire), remapped to a
// stable lowercase contract for the frontend.
type ProfileDocument struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	Path          string `json:"path"`
	SizeBytes     int64  `json:"size_bytes"`
	Source        string `json:"source"`
	ApplicationID string `json:"application_id"`
	CreatedAt     string `json:"created_at"`
	Indexed       bool   `json:"indexed"`
	IndexError    string `json:"index_error"`
	Stale         bool   `json:"stale"`
	// URL and CaptureDir are set only for a browser capture: the page it
	// was saved from, and its envelope directory in the profile's inbox.
	URL        string `json:"url"`
	CaptureDir string `json:"capture_dir"`
	// SummaryStatus is a capture's AI summary state (pending, running,
	// done, error, stalled), "" when none was asked for.
	SummaryStatus string `json:"summary_status"`
}

// KnowledgeSearchResult mirrors internal/monomind.KnowledgeResult (also
// tagless -> PascalCase on the wire).
type KnowledgeSearchResult struct {
	Path    string  `json:"path"`
	Excerpt string  `json:"excerpt"`
	Score   float64 `json:"score"`
}

// ListProfileDocuments lists the active profile's documents: uploads,
// files discovered in its folder, and browser captures (the CLI syncs the
// profile's capture inbox into the list before returning it).
func (a *App) ListProfileDocuments() ([]ProfileDocument, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var raw []struct {
		ID, Path, Filename, Source, ApplicationID, CreatedAt, IndexError, URL, CaptureDir, SummaryStatus string
		SizeBytes                                                                                        int64
		Indexed                                                                                          bool
		Stale                                                                                            bool
	}
	if err := a.runMonoCLI("", &raw, "profile", "documents", "list"); err != nil {
		return nil, err
	}
	out := make([]ProfileDocument, 0, len(raw))
	for _, d := range raw {
		out = append(out, ProfileDocument{
			ID: d.ID, Filename: d.Filename, Path: d.Path, SizeBytes: d.SizeBytes,
			Source: d.Source, ApplicationID: d.ApplicationID, CreatedAt: d.CreatedAt,
			Indexed: d.Indexed, IndexError: d.IndexError, Stale: d.Stale,
			URL: d.URL, CaptureDir: d.CaptureDir, SummaryStatus: d.SummaryStatus,
		})
	}
	return out, nil
}

// GetProfileDocument returns one document by id, scoped to the active
// profile (`profile documents get`) — a single-row counterpart to
// ListProfileDocuments for callers (chatArtifacts.js's resolveArtifact)
// that only need to re-validate one id rather than pay for and scan the
// whole vault. nil+error on not-found rather than an empty result.
func (a *App) GetProfileDocument(id string) (*ProfileDocument, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var doc ProfileDocument
	if err := a.cliJSON(profileCLITimeout, &doc, "profile", "documents", "get", id); err != nil {
		return nil, err
	}
	return &doc, nil
}

// UploadResult is UploadProfileDocument's return value.
type UploadResult struct {
	ID         string `json:"id"`
	Indexed    bool   `json:"indexed"`
	IndexError string `json:"index_error"`
}

// UploadProfileDocument registers path in the vault and indexes it for
// knowledge search. source defaults to "upload" (the CLI's own default)
// if empty. The upload itself succeeds even if indexing fails (e.g.
// monomind isn't installed or this profile isn't monomind-init'd) --
// Indexed/IndexError report that outcome so the caller can show it.
func (a *App) UploadProfileDocument(path, source string) (*UploadResult, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	args := []string{"profile", "upload-document", path}
	if source != "" {
		args = append(args, "--source", source)
	}
	var result UploadResult
	if err := a.runMonoCLI("", &result, args...); err != nil {
		return nil, err
	}
	return &result, nil
}

// DeleteProfileDocument removes a document from the vault.
func (a *App) DeleteProfileDocument(id string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	return a.runMonoCLI("", nil, "profile", "documents", "rm", id)
}

// IndexProfileDocument runs (or re-runs) knowledge_ingest for a single
// already-tracked document -- the on-demand action for a "Not indexed" or
// "Stale" row; never triggered automatically by discovery. Reuses
// UploadResult's shape exactly so the frontend can treat a manual index
// and an upload's own immediate index attempt identically.
func (a *App) IndexProfileDocument(id string) (*UploadResult, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var result UploadResult
	if err := a.runMonoCLI("", &result, "profile", "documents", "index", id); err != nil {
		return nil, err
	}
	return &result, nil
}

// SearchProfileKnowledge searches indexed profile documents (the same
// search chat uses automatically).
func (a *App) SearchProfileKnowledge(query string) ([]KnowledgeSearchResult, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	var raw []struct {
		Path, Excerpt string
		Score         float64
	}
	if err := a.runMonoCLI("", &raw, "profile", "search-knowledge", query); err != nil {
		return nil, err
	}
	out := make([]KnowledgeSearchResult, 0, len(raw))
	for _, r := range raw {
		out = append(out, KnowledgeSearchResult{Path: r.Path, Excerpt: r.Excerpt, Score: r.Score})
	}
	return out, nil
}

// OpenAnyFilePicker opens a native file picker with no extension filter
// (profile documents can be PDF, DOCX, plain text, etc.) and returns the
// selected path (empty string if cancelled).
func (a *App) OpenAnyFilePicker(title string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
	if err != nil {
		return ""
	}
	return path
}

// OpenAnyFilesPicker is OpenAnyFilePicker with multi-select: it returns every
// chosen path (empty if cancelled).
func (a *App) OpenAnyFilesPicker(title string) []string {
	if a.signedOut() != nil {
		return nil
	}
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
	if err != nil {
		return nil
	}
	a.issueDocPaths(paths)
	return paths
}

// Caps for a folder upload: choosing a whole home folder must not queue an
// unbounded import.
const (
	maxFolderFiles = 5000
	maxFolderBytes = 2 << 30
	// maxFolderVisited bounds the entries walked, documents or not.
	maxFolderVisited = 200000
	// uploadFileTimeout bounds one `profile upload-document` call (copy + index).
	uploadFileTimeout = 5 * time.Minute
)

// folderWalk is walkFolderFiles' result.
type folderWalk struct {
	files       []string       // document files to upload
	byExt       map[string]int // files per extension
	total       int64          // bytes in files
	skipped     []string       // first few hidden, symlinked or unreadable entries
	skippedN    int            // all of them
	unsupported int            // regular files that are not documents (docscan.IsDocumentFile)
	over        bool           // caps passed; the walk stopped
}

const maxSkippedListed = 10

// walkFolderFiles lists the document files under dir, recursively, using the
// same allowlist as the profile folder scan (docscan.IsDocumentFile), so keys,
// config and code never enter the vault. Hidden entries (dot-names below dir;
// dir itself may be dot-named), symlinks and unreadable entries are skipped.
func walkFolderFiles(dir string) folderWalk {
	w := folderWalk{byExt: map[string]int{}}
	stop := errors.New("over cap")
	skip := func(p string) {
		w.skippedN++
		if len(w.skipped) < maxSkippedListed {
			w.skipped = append(w.skipped, p)
		}
	}
	visited := 0
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if visited++; visited > maxFolderVisited {
			w.over = true
			return stop
		}
		if err != nil {
			skip(p)
			return nil
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			skip(p)
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if !d.Type().IsRegular() || ierr != nil {
			skip(p)
			return nil
		}
		if !docscan.IsDocumentFile(d.Name()) {
			w.unsupported++
			return nil
		}
		w.files = append(w.files, p)
		w.byExt[strings.ToLower(strings.TrimPrefix(filepath.Ext(d.Name()), "."))]++
		w.total += info.Size()
		if len(w.files) > maxFolderFiles || w.total > maxFolderBytes {
			w.over = true
			return stop
		}
		return nil
	})
	return w
}

// OpenFolderFilesPicker opens a folder picker and returns the document files
// to upload from the chosen folder, after the user confirms. Empty if
// cancelled, declined, empty or over the caps.
func (a *App) OpenFolderFilesPicker(title string) []string {
	if a.signedOut() != nil {
		return nil
	}
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
	if err != nil || dir == "" {
		return nil
	}
	info := func(t, m string) {
		_, _ = runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{Type: runtime.InfoDialog, Title: t, Message: m})
	}
	w := walkFolderFiles(dir)
	if w.over {
		info("Folder too large", fmt.Sprintf("%s holds more than %d documents or %d GB. Choose a smaller folder.", dir, maxFolderFiles, maxFolderBytes>>30))
		return nil
	}
	if len(w.files) == 0 {
		info("Nothing to upload", fmt.Sprintf("%s has no supported documents (%d other files, %d hidden or unreadable entries ignored).", dir, w.unsupported, w.skippedN))
		return nil
	}
	exts := make([]string, 0, len(w.byExt))
	for e, n := range w.byExt {
		exts = append(exts, fmt.Sprintf("%s ×%d", e, n))
	}
	sort.Strings(exts)
	msg := fmt.Sprintf("Upload %d documents (%.1f MB) from %s?\n%s\n\nUploaded documents are copied into your profile and become searchable by chat, form-fill and AI agents.", len(w.files), float64(w.total)/(1<<20), dir, strings.Join(exts, ", "))
	if w.unsupported > 0 {
		msg += fmt.Sprintf("\n\n%d files of other types will be ignored.", w.unsupported)
	}
	if w.skippedN > 0 {
		msg += fmt.Sprintf("\n%d hidden, symlinked or unreadable entries will be skipped, e.g.:\n%s", w.skippedN, strings.Join(w.skipped, "\n"))
	}
	choice, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type: runtime.QuestionDialog, Title: "Upload folder", Message: msg,
		Buttons: []string{"Upload", "Cancel"}, DefaultButton: "Cancel", CancelButton: "Cancel",
	})
	if err != nil || choice != "Upload" {
		return nil
	}
	a.issueDocPaths(w.files)
	return w.files
}

// BatchUploadResult is UploadProfileDocuments' return value.
type BatchUploadResult struct {
	Uploaded  int      `json:"uploaded"`
	Unindexed int      `json:"unindexed"`
	Failed    []string `json:"failed"` // "path: reason"
}

// issueDocPaths records the paths a picker handed to the frontend,
// replacing any earlier set: UploadProfileDocuments takes only these.
func (a *App) issueDocPaths(paths []string) {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	a.pickedDocsMu.Lock()
	a.pickedDocs = set
	a.pickedDocsMu.Unlock()
}

// takeDocPaths returns the issued set and clears it, so a batch consumes it.
func (a *App) takeDocPaths() map[string]bool {
	a.pickedDocsMu.Lock()
	defer a.pickedDocsMu.Unlock()
	set := a.pickedDocs
	a.pickedDocs = nil
	return set
}

// UploadProfileDocuments uploads paths -- only ones a picker just issued --
// into the profile that is selected when it is called, resolved once, so
// switching profiles mid-batch cannot misfile the rest. One batch runs at a
// time.
func (a *App) UploadProfileDocuments(paths []string) (*BatchUploadResult, error) {
	if err := a.signedOut(); err != nil {
		return nil, err
	}
	id, err := a.profileID()
	if err != nil {
		return nil, err
	}
	return a.uploadBatch(paths, func(ctx context.Context, path string) (UploadResult, error) {
		var one UploadResult
		args, err := scopedCLIArgs(id, "profile", "upload-document", path)
		if err == nil {
			err = a.runCLIArgsCtx(ctx, "", &one, args)
		}
		return one, err
	})
}

// uploadBatch runs upload for each path that a picker issued and that is
// still a regular file (not a symlink, FIFO or device), each under its own
// timeout so the busy flag always releases.
func (a *App) uploadBatch(paths []string, upload func(ctx context.Context, path string) (UploadResult, error)) (*BatchUploadResult, error) {
	if !a.docBatchBusy.CompareAndSwap(false, true) {
		return nil, errors.New("an upload is already running")
	}
	defer a.docBatchBusy.Store(false)
	issued := a.takeDocPaths()
	res := &BatchUploadResult{Failed: []string{}}
	fail := func(path string, err error) {
		res.Failed = append(res.Failed, fmt.Sprintf("%s: %v", filepath.Base(path), err))
	}
	for _, path := range paths {
		if !issued[path] {
			fail(path, errors.New("not chosen in the file picker"))
			continue
		}
		if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
			fail(path, errors.New("not a regular file"))
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), uploadFileTimeout)
		one, err := upload(ctx, path)
		cancel()
		switch {
		case err != nil:
			fail(path, err)
		case !one.Indexed:
			res.Unindexed++
			res.Uploaded++
		default:
			res.Uploaded++
		}
	}
	return res, nil
}
