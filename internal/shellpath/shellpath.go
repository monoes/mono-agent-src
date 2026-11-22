// Package shellpath recovers the user's login-shell PATH for processes that
// were not started from a terminal.
//
// A GUI app launched via Finder/Dock/`open` (or a desktop launcher on Linux)
// inherits launchd's minimal PATH (/usr/bin:/bin:/usr/sbin:/sbin) — none of
// the entries nvm, Homebrew, volta, asdf etc. add from shell rc files. Every
// Node-based tool the app shells out to (monomind, claude, npm) is then
// either invisible or, worse, found but unable to start because its
// `#!/usr/bin/env node` shebang can't resolve `node` (exit status 127).
package shellpath

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	startMarker = "__MONOAGENT_PATH_START__"
	endMarker   = "__MONOAGENT_PATH_END__"
)

// LoginPath asks the user's login shell for its PATH. Markers fence the
// value so banner/motd noise printed by rc files is ignored.
func LoginPath(ctx context.Context) (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
		if runtime.GOOS != "darwin" {
			shell = "/bin/sh"
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// -i so interactive-only rc files (.zshrc/.bashrc, where nvm lives)
	// are sourced; -l for the profile files (Homebrew shellenv).
	cmd := exec.CommandContext(ctx, shell, "-ilc", printPathCommand(shell))
	// rc files can spawn background helpers that hold stdout open; don't
	// let them keep Output blocked past the timeout.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return "", err
	}
	return extract(string(out)), nil
}

// printPathCommand prints PATH colon-joined between the markers. fish keeps
// PATH as a list and "$PATH" there joins with spaces, so it needs its own form.
func printPathCommand(shell string) string {
	if filepath.Base(shell) == "fish" {
		return `printf '%s%s%s' "` + startMarker + `" (string join : $PATH) "` + endMarker + `"`
	}
	return `printf '%s%s%s' "` + startMarker + `" "$PATH" "` + endMarker + `"`
}

func extract(out string) string {
	i := strings.LastIndex(out, startMarker)
	if i < 0 {
		return ""
	}
	rest := out[i+len(startMarker):]
	j := strings.Index(rest, endMarker)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:j])
}

// Merge returns front's entries followed by any of back's not already
// present, preserving order and dropping empties.
func Merge(front, back string) string {
	seen := map[string]bool{}
	var parts []string
	for _, list := range []string{front, back} {
		for _, p := range filepath.SplitList(list) {
			if p == "" || !filepath.IsAbs(p) || seen[p] { // a relative entry makes the working folder a command folder
				continue
			}
			seen[p] = true
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

// Apply merges the login-shell PATH into this process's PATH so every child
// process inherits it. A no-op on Windows (GUI apps get the full user PATH
// there) and best-effort everywhere else: on any failure PATH is unchanged.
func Apply(ctx context.Context) {
	if runtime.GOOS == "windows" {
		return
	}
	login, err := LoginPath(ctx)
	if err != nil || login == "" {
		return
	}
	_ = os.Setenv("PATH", Merge(login, os.Getenv("PATH")))
}

// Prepend puts dir at the front of this process's PATH if it is not already
// on it.
func Prepend(dir string) {
	cur := os.Getenv("PATH")
	for _, p := range filepath.SplitList(cur) {
		if p == dir {
			return
		}
	}
	_ = os.Setenv("PATH", Merge(dir, cur))
}
