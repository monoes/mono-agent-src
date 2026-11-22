// wails-app/app_files.go
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"

	"github.com/monoes/mono-agent/internal/fsconfine"
)

// documentPath resolves id to its on-disk path, scoped to the active
// profile (`profile documents get`).
func (a *App) documentPath(id string) (string, error) {
	doc, err := a.GetProfileDocument(id)
	if err != nil {
		return "", err
	}
	return doc.Path, nil
}

// maxInlinePreviewBytes caps GetProfileDocumentData's in-memory base64
// encode. Uploaded documents were always small and user-chosen; discovery
// (internal/docscan) can now surface anything already sitting in the
// profile folder, including files far larger than anyone would have
// picked from a file dialog.
const maxInlinePreviewBytes = 25 * 1024 * 1024

// GetProfileDocumentData reads a vault document from disk and returns it as
// a base64 data URL (e.g. "data:application/pdf;base64,..."), the same
// technique GetVaultImageData uses for images — the reliable way to load
// local file content into the Wails WebView for binary preview (images,
// PDFs) without a file:// URL, which Wails' BrowserOpenURL explicitly
// rejects and which the webview's CSP may also block.
func (a *App) GetProfileDocumentData(id string) (string, error) {
	if err := a.signedOut(); err != nil {
		return "", err
	}
	path, err := a.documentPath(id)
	if err != nil {
		return "", err
	}
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > maxInlinePreviewBytes {
		return "", fmt.Errorf("file too large to preview inline (%d MB) — open it with your system's default application instead", fi.Size()/1024/1024)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading document file: %w", err)
	}
	mimeType := mime.TypeByExtension(filepath.Ext(path))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// GetProfileDocumentText reads a vault document from disk as UTF-8 text,
// for file types previewed as plain text/source (txt, md, html, csv,
// json, ...) rather than as a binary data URL.
func (a *App) GetProfileDocumentText(id string) (string, error) {
	if err := a.signedOut(); err != nil {
		return "", err
	}
	path, err := a.documentPath(id)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading document file: %w", err)
	}
	return string(data), nil
}

// OpenPathWithOS hands path to the operating system's default file-open
// mechanism — the same as double-clicking it in a file manager. Used as the
// fallback when no in-app viewer exists for a file's type.
func (a *App) OpenPathWithOS(path string) error {
	if err := a.signedOut(); err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("file not found: %w", err)
	}
	path, err := confineOpenPath(path, a.openableRoots())
	if err != nil {
		return err
	}
	name, args := openFileCommand(goruntime.GOOS, path)
	// Start, not Run: the launched app (e.g. a text editor or Office) may
	// stay open indefinitely, so this only reports launch failures (missing
	// binary, ...), not the opened app's own exit code — same fire-and-forget
	// shape as app_update.go's install scripts.
	opener := exec.Command(name, args...)
	suppressConsole(opener)
	return opener.Start()
}

// launchableExts are file types the OS would run rather than show, on any
// platform: OpenPathWithOS never hands these to the default opener.
var launchableExts = map[string]bool{
	".exe": true, ".bat": true, ".cmd": true, ".com": true, ".lnk": true,
	".ps1": true, ".msi": true, ".scr": true, ".vbs": true, ".js": true,
	".jar": true, ".desktop": true, ".sh": true, ".command": true,
	".app": true, ".appimage": true, ".run": true, ".pif": true,
}

// openableRoots are the folders OpenPathWithOS may open files from: every
// profile's folder and the chat attachment spool.
func (a *App) openableRoots() []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".monoagent", "chat-spool"))
	}
	var rows []cliProfile
	if err := a.listProfilesInto(&rows); err == nil {
		for _, p := range rows {
			if p.RootDir != "" {
				roots = append(roots, p.RootDir)
			}
		}
	}
	return roots
}

// confineOpenPath resolves path (following symlinks) and returns it only if
// it is a regular, non-launchable file inside one of roots. The returned
// path, not the input, is what must be opened.
func confineOpenPath(path string, roots []string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("refusing to open %q: not an absolute path", path)
	}
	for _, root := range roots {
		resolved, err := fsconfine.Path(fsconfine.WithRoot(context.Background(), root), path)
		if err != nil {
			continue
		}
		fi, err := os.Stat(resolved)
		if err != nil || !fi.Mode().IsRegular() {
			return "", fmt.Errorf("refusing to open %q: not a regular file", path)
		}
		if launchableExts[strings.ToLower(filepath.Ext(resolved))] {
			return "", fmt.Errorf("refusing to open %q: executable file types are not opened from the app", path)
		}
		return resolved, nil
	}
	return "", fmt.Errorf("refusing to open %q: outside the profile folders", path)
}

// openFileCommand returns the OS-appropriate command name and arguments to
// open path with its default associated application: "open" on darwin,
// "cmd /c start" on windows (an empty title arg is required so `start`
// doesn't mistake a quoted path for the window title), and "xdg-open"
// everywhere else. Split out from OpenPathWithOS, mirroring
// revealFolderCommand in app.go, so tests can exercise the per-GOOS command
// selection without launching a real application.
func openFileCommand(goos, path string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{path}
	case "windows":
		return "cmd", []string{"/c", "start", "", path}
	default:
		return "xdg-open", []string{path}
	}
}
