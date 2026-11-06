package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// ─────────────────────────────────────────────────────────────────────────────
// Per-profile monomind initialization. Like every other binding it goes
// through monoagentcli: `doctor fix monomind.profile_init` runs `monomind
// init` in the profile folder (and the one `claude -p` turn that registers
// it with Claude Code), so the GUI and the CLI set a folder up the same way.
// ─────────────────────────────────────────────────────────────────────────────

// fixMonomindProfileInit is the doctor fix that sets a profile folder up
// (health.FixMonomindProfileInit).
const fixMonomindProfileInit = "monomind.profile_init"

// monomindInitTimeout bounds the fix: monomind's own init deadline (10
// minutes) plus the Claude registration turn.
const monomindInitTimeout = 12 * time.Minute

// isMonomindInitializedAt reports whether root was set up by `monomind
// init`: the marker monomind's own CLI uses, .monomind/config.yaml (a bare
// .monomind/ doesn't count — profiledir.EnsureLayout creates it). Same
// test as monomind.IsInitializedAt, which this file doesn't import.
func isMonomindInitializedAt(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".monomind", "config.yaml"))
	return err == nil
}

// IsMonomindInitialized reports whether the active profile's folder has
// already been set up by `monomind init` — a direct file check, not a
// subprocess call, since this is checked on every Orgs/Agents tab load.
func (a *App) IsMonomindInitialized() bool {
	if a.signedOut() != nil {
		return false
	}
	id, err := a.profileID()
	if err != nil {
		return false
	}
	return isMonomindInitializedAt(profiledir.Root(a.db, id))
}

// emitMonomindInitEvent reports init progress to the frontend. kind is
// "line" (a streamed stdout/stderr line), "error" (init failed — message is
// the error text), or "done" (init finished successfully).
func (a *App) emitMonomindInitEvent(kind, message string) {
	if a.ctx == nil {
		return
	}
	eventsEmit(a.ctx, "monomind:initProgress", map[string]interface{}{
		"kind":    kind,
		"message": message,
	})
}

// InitializeMonomindProfile runs `monoagentcli doctor fix
// monomind.profile_init` for the active profile, streaming its progress
// via monomind:initProgress events and returning immediately (it can take
// minutes). A failure to start is both returned and emitted, since the
// frontend waits for the event.
func (a *App) InitializeMonomindProfile() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	fail := func(err error) string {
		a.emitMonomindInitEvent("error", err.Error())
		return aiError(err)
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return fail(err)
	}
	// Shares the health page's run registry: the same fix started from
	// there and from here would run `monomind init` twice at once, and it
	// makes this run cancellable (CancelHealthRun) like the page's fixes.
	key := runKey(fixMonomindProfileInit)
	run, ctx, ok := beginHealthRun(a.parentCtx(), key, monomindInitTimeout)
	if !ok {
		return fail(fmt.Errorf("this profile's folder is already being set up"))
	}
	pid, err := a.profileID()
	if err != nil {
		endHealthRun(key, run)
		return fail(err)
	}
	args, err := healthFixArgs(pid, fixMonomindProfileInit)
	if err != nil {
		endHealthRun(key, run)
		return fail(err)
	}
	go func() {
		defer endHealthRun(key, run)
		a.runMonomindInit(ctx, cliBin, args)
	}()
	return `{"ok":true}`
}

// runMonomindInit runs the fix and relays its NDJSON events.
func (a *App) runMonomindInit(ctx context.Context, cliBin string, args []string) {
	cmd := exec.CommandContext(ctx, cliBin, args...)
	suppressConsole(cmd)
	stopGracefully(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		a.emitMonomindInitEvent("error", err.Error())
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		a.emitMonomindInitEvent("error", err.Error())
		return
	}
	finished := relayFixEvents(stdout, a.emitMonomindInitEvent)
	waitErr := cmd.Wait()
	if !finished {
		// The CLI died without its final event (killed, timed out, crashed).
		msg := "setting up monomind stopped without reporting a result"
		if waitErr != nil {
			msg = waitErr.Error()
		}
		if s := strings.TrimSpace(stderr.String()); s != "" {
			msg += ": " + s
		}
		a.emitMonomindInitEvent("error", msg)
	}
}

// relayFixEvents maps `doctor fix --json` NDJSON ({"kind":"line"|"done"|
// "error","message"}) onto emit, a non-JSON line becoming a "line", and
// reports whether a final done/error event came. "done" is emitted without
// a message, as the frontend has always received it.
func relayFixEvents(r io.Reader, emit func(kind, message string)) (finished bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Kind == "" {
			emit("line", sc.Text())
			continue
		}
		switch ev.Kind {
		case "done":
			finished = true
			emit("done", "")
		case "error":
			finished = true
			emit("error", ev.Message)
		default:
			emit("line", ev.Message)
		}
	}
	if sc.Err() != nil {
		// A line too long for the scanner: keep draining so the CLI isn't
		// blocked writing to a full pipe.
		emit("line", "(output line too long to show)")
		_, _ = io.Copy(io.Discard, r)
	}
	return finished
}
