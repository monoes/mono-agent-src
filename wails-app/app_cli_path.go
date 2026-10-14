package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Action Execution
// ─────────────────────────────────────────────────────────────────────────────

// findMonoAgentCLI locates the monoagentcli binary.
func findMonoAgentCLI() (string, error) {
	// Explicit override — checked first so a stale PATH-installed
	// monoagentcli (e.g. an old system-wide `go install`/release binary
	// that predates a given dev build's new commands) can't silently shadow
	// the binary a developer actually wants `wails dev` to shell out to.
	if p := strings.TrimSpace(os.Getenv("MONOAGENTCLI_BIN")); p != "" {
		if fileExists(p) {
			return p, nil
		}
	}
	// Also check relative to executable (bundled app or dev bin/ directory).
	// Checked before PATH so a sibling binary built with the app is preferred over
	// an older version installed globally in ~/.local/bin or /usr/local/bin.
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		if p, ok := siblingCLI(filepath.Dir(exe), goruntime.GOOS, goruntime.GOARCH); ok {
			return p, nil
		}
	}

	if p, err := exec.LookPath("monoagentcli"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	name := cliSiblingNames(goruntime.GOOS, goruntime.GOARCH)[0]
	candidates := []string{
		filepath.Join(home, "go", "bin", name),
		filepath.Join(home, ".local", "bin", name),
		"/usr/local/bin/monoagentcli",
		"/opt/homebrew/bin/monoagentcli",
	}
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		execDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(execDir, "..", "..", "..", "cmd", "monoagentcli", "monoagentcli"),
		)
	}
	for _, p := range candidates {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("monoagentcli binary not found — run `go install` or place the binary in PATH")
}

// cliSiblingNames are the file names the CLI has next to the desktop app:
// "monoagentcli" (".exe" on Windows; macOS .app bundles and the current
// Linux tarball), then the release's bundled name
// "monoagentcli-<goos>-<goarch>-bundled[.exe]" (older Linux tarballs, and
// the separate Windows download saved beside MonoAgent-windows-amd64.exe).
func cliSiblingNames(goos, goarch string) []string {
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	return []string{
		"monoagentcli" + ext,
		"monoagentcli-" + goos + "-" + goarch + "-bundled" + ext,
	}
}

// siblingCLI returns the first CLI found in execDir under cliSiblingNames.
func siblingCLI(execDir, goos, goarch string) (string, bool) {
	for _, n := range cliSiblingNames(goos, goarch) {
		if p := filepath.Join(execDir, n); fileExists(p) {
			return p, true
		}
	}
	return "", false
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// nodeTypeToPlatform derives the connection platform ID from a node type string.
// e.g. "service.google_sheets" → "google_sheets", "db.postgres" → "postgresql"
func nodeTypeToPlatform(nodeType string) string {
	overrides := map[string]string{
		"db.postgres":     "postgresql",
		"db.mysql":        "mysql",
		"db.mongodb":      "mongodb",
		"db.redis":        "redis",
		"comm.email_send": "smtp",
		"comm.email_read": "imap",
	}
	if p, ok := overrides[nodeType]; ok {
		return p
	}
	parts := strings.SplitN(nodeType, ".", 2)
	if len(parts) == 2 {
		return parts[1] // "service.google_sheets" → "google_sheets"
	}
	return nodeType
}
