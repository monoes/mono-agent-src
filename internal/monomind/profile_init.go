package monomind

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// InitTimeout bounds `monomind init`: it copies skill/command files and may
// shell out further (npx) itself — minutes, not seconds.
const InitTimeout = 10 * time.Minute

// initErrorTailLines is how much of init's output a failure message keeps.
const initErrorTailLines = 5

// IsInitializedAt reports whether root has been set up by `monomind init` —
// the same on-disk marker monomind's own CLI uses (.monomind/config.yaml).
// A bare .monomind/ does not count: profiledir.EnsureLayout creates it.
func IsInitializedAt(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".monomind", "config.yaml"))
	return err == nil
}

// InitOptions configures InitProfile.
type InitOptions struct {
	Root     string            // folder to initialize (a profile root)
	Repin    bool              // --force: refresh monomind's own files, .mcp.json's version pin included
	Progress func(line string) // receives every output line; may be nil
	Prepare  func(*exec.Cmd)   // platform tweaks, e.g. hiding the window; may be nil
}

// InitProfile runs `monomind init` in opts.Root, streaming its output, then
// registers the folder with Claude Code's project list (best effort).
func InitProfile(ctx context.Context, opts InitOptions) error {
	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}
	bin, err := findIn(opts.Root)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, InitTimeout)
	defer cancel()

	// --yes suppresses the "already initialized, reinitialize?" prompt;
	// --no-watch avoids leaving a background monograph watcher running;
	// --no-install skips a potential global `npm install -g
	// @anthropic-ai/claude-code`. CI=true makes every prompt path treat
	// this as non-interactive even if monomind's TTY check changes.
	// --force (Repin) merges monomind's own server entry into .mcp.json (the
	// user's other entries are kept); an edited managed block in CLAUDE.md or
	// AGENTS.md is replaced after a backup in .monomind/backups/. Without it
	// an existing .mcp.json is left alone.
	args := []string{"init", "--yes", "--no-watch", "--no-install"}
	if opts.Repin {
		args = append(args, "--force")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if opts.Prepare != nil {
		opts.Prepare(cmd)
	}
	cmd.Dir = opts.Root // init has no --project flag and ignores MONOMIND_CWD
	cmd.Env = PinEnvIn(append(ScrubDestinations(os.Environ()), "CI=true"), bin, opts.Root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var tail []string
	for sc.Scan() {
		line := sc.Text()
		progress(line)
		if strings.TrimSpace(line) != "" {
			tail = append(tail, line)
			if len(tail) > initErrorTailLines {
				tail = tail[1:]
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return errors.New(InitFailureMessage(bin, err, tail))
	}
	if !registerViaInit(ctx, bin, opts.Root, opts.Prepare, progress) {
		registerClaudeCodeProject(ctx, opts.Root, opts.Prepare, progress)
	}
	return nil
}

// registerViaInit registers root with Claude Code's project list by
// `monomind init --register-claude-project --json` (capability init-json):
// it creates ~/.claude/projects/<slug>/ itself, with no model turn and no
// login, in about a second (--if-missing leaves every file as it is). It
// reports false when this monomind lacks the capability or the call failed,
// or the folder did not end up registered, so the caller falls back to
// registerClaudeCodeProject.
func registerViaInit(ctx context.Context, bin, root string, prepare func(*exec.Cmd), progress func(string)) bool {
	vi, err := Handshake(ctx, bin)
	if err != nil || !vi.HasCapability(CapInitJSON) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "init", "--project", root, "--if-missing", "--json", "--no-graph",
		"--register-claude-project", "--yes", "--no-watch", "--no-install")
	if prepare != nil {
		prepare(cmd)
	}
	cmd.Dir = root
	cmd.Env = PinEnvIn(append(ScrubDestinations(os.Environ()), "CI=true"), bin, root)
	out, err := cmd.Output()
	var res struct {
		Registered bool `json:"claude_project_registered"`
	}
	if err != nil || json.Unmarshal(lastJSONLine(out), &res) != nil {
		return false
	}
	if !res.Registered {
		return false
	}
	progress("Registered with Claude Code's project list.")
	return true
}

// InitFailureMessage turns a failed `monomind init` into something
// actionable: the binary, the exit error, the last output lines, and a hint
// for exit 127 (the shebang's `node` could not be resolved).
func InitFailureMessage(bin string, err error, tail []string) string {
	msg := err.Error() + " (" + bin + ")"
	var xerr *exec.ExitError
	if errors.As(err, &xerr) && xerr.ExitCode() == 127 {
		msg += " — a command it needs was not found (usually `node`: is Node on your login shell's PATH?)"
	}
	if len(tail) > 0 {
		msg += "\n" + strings.Join(tail, "\n")
	}
	return msg
}

// registerClaudeCodeProject makes root show up in monomind's web dashboard
// project list. That list is sourced from ~/.claude/projects/<slug>/, the
// per-directory folder Claude Code creates the first time `claude` runs
// there. This is the fallback for a monomind without capability init-json
// (see registerViaInit): one lightweight --print turn is enough. Best
// effort: failures are reported as progress lines only.
func registerClaudeCodeProject(ctx context.Context, root string, prepare func(*exec.Cmd), progress func(string)) {
	claudeBin, err := exec.LookPath("claude")
	if err != nil {
		progress("(skipped: claude CLI not found on PATH — this profile won't appear in monomind's dashboard project list until a Claude Code session is opened here)")
		return
	}
	// It runs in root: pinned like monomind, so a shim can't let the
	// profile folder pick it (#301).
	if abs, err := filepath.Abs(claudeBin); err == nil {
		claudeBin = abs
	}
	if claudeBin, err = pinPath(claudeBin, "claude"); err == nil {
		err = CheckOutside(claudeBin, root)
	}
	if err != nil {
		progress("(skipped: claude CLI can't be pinned: " + err.Error() + ")")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBin, "-p", "monomind initialized")
	cmd.Env = PinEnvIn(ScrubDestinations(os.Environ()), claudeBin, root)
	if prepare != nil {
		prepare(cmd)
	}
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		progress("(claude CLI registration step failed, non-fatal: " + err.Error() + ")")
		return
	}
	progress("Registered with Claude Code's project list.")
}
