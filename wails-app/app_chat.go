package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// ─────────────────────────────────────────────────────────────────────────────
// Chat supervisor (docs/mastermind/plans/2026-09-11-interactive-agent-chat.md)
//
// The chat's history lives behind `monoagentcli chat` (issue #172): every
// read, write and delete of conversations, turns and events is a CLI call,
// and a turn is one `monoagentcli chat --conversation C --turn T -- <msg>`
// process that creates its turn row and journals its own events. What stays
// here is supervising those processes, which only the UI process can do:
// admission (one active turn per conversation), the process group a Stop
// kills, the instance id that tells this window's turns from another
// window's, relaying each committed event to the frontend as "chat:event",
// and finishing a turn (through `chat history finish`) whose process died
// before it could.
//
// Startup reconciliation (`chat history reconcile`) marks every turn left
// active as interrupted, except this instance's own. It cannot tell a dead
// previous instance from a second live one (no liveness signal exists); for
// a single-user desktop app sharing one SQLite file that is an edge case.
// ─────────────────────────────────────────────────────────────────────────────

// chatProcess abstracts a running `monoagentcli chat` subprocess so tests
// can inject a fake one without a real binary. Stdout/Stderr must be safe
// to read concurrently with each other (they are, in the real
// exec.Cmd-backed implementation: separate pipes).
type chatProcess interface {
	Stdout() io.Reader
	Stderr() io.Reader
	// Wait blocks until the process exits and returns its error (nil on a
	// clean exit), mirroring exec.Cmd.Wait.
	Wait() error
	// Kill terminates the process (and, where supported, its process
	// group) immediately. Safe to call after the process has already
	// exited.
	Kill()
}

// realChatProcess wraps a real *exec.Cmd, using the process-group helpers
// in proc_unix.go/proc_windows.go.
type realChatProcess struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	stderr io.ReadCloser
}

func (p *realChatProcess) Stdout() io.Reader { return p.stdout }
func (p *realChatProcess) Stderr() io.Reader { return p.stderr }
func (p *realChatProcess) Wait() error       { return waitChatProcess(p.cmd) }
func (p *realChatProcess) Kill()             { killChatProcessGroup(p.cmd) }

// chatProcessLauncher starts one `monoagentcli chat ...` turn. Injectable so
// tests can drive the supervisor against a fake process.
type chatProcessLauncher func(ctx context.Context, cliBin string, args []string) (chatProcess, error)

func defaultChatProcessLauncher(ctx context.Context, cliBin string, args []string) (chatProcess, error) {
	cmd := exec.CommandContext(ctx, cliBin, args...)
	setChatProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &realChatProcess{cmd: cmd, stdout: stdout, stderr: stderr}, nil
}

// chatEventEmitter delivers one committed event live to the frontend.
// Injectable so tests never need a real Wails runtime context.
type chatEventEmitter func(ev chatevents.Event)

// chatTurnKey identifies one turn's registry slot.
type chatTurnKey struct{ conversationID, turnID string }

// chatTurnHandle is the in-memory state for one admitted, currently-active
// turn, removed from the registry once the turn finishes.
type chatTurnHandle struct {
	profileID      string // captured at admission, immutable for the turn's life
	conversationID string
	turnID         string
	mission        bool // the user confirmed a mission proposal: the host send gets coder sessions send --mission

	cancel context.CancelFunc

	host bool // run by the coder host: the app only relays its journal and sends it commands

	mu            sync.Mutex
	stopRequested bool
	proc          chatProcess // nil until the subprocess is launched
}

func (h *chatTurnHandle) requestStop() {
	h.mu.Lock()
	already := h.stopRequested
	h.stopRequested = true
	proc := h.proc
	cancel := h.cancel
	h.mu.Unlock()
	if already {
		return
	}
	if cancel != nil {
		cancel()
	}
	if proc != nil {
		proc.Kill()
	}
}

func (h *chatTurnHandle) isStopRequested() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.stopRequested
}

// chatCLIError is a failed `monoagentcli` call: its exit code and the error
// line it printed.
type chatCLIError struct {
	code int
	msg  string
}

func (e *chatCLIError) Error() string { return e.msg }

// chatCLIExitCode returns err's CLI exit code, or 0 when err isn't one.
func chatCLIExitCode(err error) int {
	var ce *chatCLIError
	if errors.As(err, &ce) {
		return ce.code
	}
	return 0
}

// lastLine returns s's last non-empty line: the CLI prints its error last,
// after any warnings.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// chatCLITimeout bounds one history call (the CLI migrates the DB on open).
const chatCLITimeout = 60 * time.Second

// chatAdmissionTimeout bounds how long StartChatTurn waits for the turn
// process to register its turn.
const chatAdmissionTimeout = 60 * time.Second

// chatSupervisor owns admission and the per-turn registry. Independently
// constructible (no Wails runtime/App required) so it is fully
// unit-testable — see app_chat_test.go.
type chatSupervisor struct {
	launcher chatProcessLauncher
	emit     chatEventEmitter
	findCLI  func() (string, error)
	// instanceID identifies this running app process, recorded as a turn's
	// owner — distinguishes "my own turn, stoppable here" from "another
	// live instance's turn, read-only here" (plan §236). Fresh per process
	// start.
	instanceID string

	mu                   sync.Mutex
	stopping             bool // shutdown prevents late queued admissions
	turns                map[chatTurnKey]*chatTurnHandle
	activeByConversation map[string]chatTurnKey // one active turn per conversation
	afterConversation    map[string]func()      // one operator-requested mission continuation after a planning turn
	running              sync.WaitGroup         // turn relays still finishing
	cwds                 sync.Map               // profile+"\x00"+conversation → its coder folder (conversationCwd)
}

func newChatSupervisor(launcher chatProcessLauncher, emit chatEventEmitter, findCLI func() (string, error)) *chatSupervisor {
	return &chatSupervisor{
		launcher:             launcher,
		emit:                 emit,
		findCLI:              findCLI,
		instanceID:           uuid.NewString(),
		turns:                make(map[chatTurnKey]*chatTurnHandle),
		activeByConversation: make(map[string]chatTurnKey),
		afterConversation:    make(map[string]func()),
	}
}

// cli runs `monoagentcli --profile P --json <args>` and decodes its stdout
// into result (when non-nil). An empty profileID is errNoProfile.
func (sup *chatSupervisor) cli(profileID string, result any, args ...string) error {
	full, err := scopedCLIArgs(profileID, args...)
	if err != nil {
		return err
	}
	return sup.runCLI(full, result, args)
}

// globalCLI runs a machine-wide `monoagentcli --json <args>` (no profile).
func (sup *chatSupervisor) globalCLI(result any, args ...string) error {
	return sup.runCLI(unscopedCLIArgs(args...), result, args)
}

// runCLI runs full and decodes its stdout into result; args only name the
// command in error messages.
func (sup *chatSupervisor) runCLI(full []string, result any, args []string) error {
	bin, err := sup.findCLI()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatCLITimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, full...)
	suppressConsole(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			msg := lastLine(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return &chatCLIError{code: ee.ExitCode(), msg: msg}
		}
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(out, result); err != nil {
		return fmt.Errorf("monoagentcli %s: unexpected output: %w", strings.Join(args, " "), err)
	}
	return nil
}

// reconcileOrphanedTurns marks every turn left active by a previous
// instance as interrupted, via `chat history reconcile`. This instance's
// own turns are skipped, so it may run concurrently with the first Start.
func (sup *chatSupervisor) reconcileOrphanedTurns() []error {
	if err := sup.globalCLI(nil, "chat", "history", "reconcile", "--except-owner", sup.instanceID); err != nil {
		return []error{err}
	}
	return nil
}

// ─── Admission ──────────────────────────────────────────────────────────────

var errChatBusy = fmt.Errorf("busy")

// admit registers turnID as the active turn for conversationID, or reports
// busy/duplicate. Returns the existing handle (idempotent re-admission) when
// turnID matches the conversation's already-active turn.
func (sup *chatSupervisor) admit(conversationID, turnID string) (h *chatTurnHandle, alreadyActive bool, err error) {
	sup.mu.Lock()
	defer sup.mu.Unlock()
	if sup.stopping {
		return nil, false, errChatBusy
	}
	if key, ok := sup.activeByConversation[conversationID]; ok {
		if key.turnID == turnID {
			return sup.turns[key], true, nil
		}
		return nil, false, errChatBusy
	}
	key := chatTurnKey{conversationID: conversationID, turnID: turnID}
	h = &chatTurnHandle{conversationID: conversationID, turnID: turnID}
	sup.turns[key] = h
	sup.activeByConversation[conversationID] = key
	return h, false, nil
}

// release clears a finished turn's admission slot.
func (sup *chatSupervisor) release(conversationID, turnID string) {
	sup.mu.Lock()
	key := chatTurnKey{conversationID: conversationID, turnID: turnID}
	h := sup.turns[key]
	delete(sup.turns, key)
	var after func()
	if cur, ok := sup.activeByConversation[conversationID]; ok && cur == key {
		delete(sup.activeByConversation, conversationID)
		after = sup.afterConversation[conversationID]
		delete(sup.afterConversation, conversationID)
	}
	sup.mu.Unlock()
	// A stop or app shutdown cancels the pending continuation too.
	if after != nil && (h == nil || !h.isStopRequested()) {
		after()
	}
}

// afterCurrent queues one continuation only when a turn is already running.
func (sup *chatSupervisor) afterCurrent(conversationID string, after func()) bool {
	sup.mu.Lock()
	defer sup.mu.Unlock()
	if sup.stopping {
		return false
	}
	if _, active := sup.activeByConversation[conversationID]; !active {
		return false
	}
	if sup.afterConversation[conversationID] == nil {
		sup.afterConversation[conversationID] = after
	}
	return true
}

func (sup *chatSupervisor) lookup(conversationID, turnID string) *chatTurnHandle {
	sup.mu.Lock()
	defer sup.mu.Unlock()
	return sup.turns[chatTurnKey{conversationID: conversationID, turnID: turnID}]
}

// stopAll requests stop on every registered turn — called at app shutdown
// so no orphaned subprocess survives the GUI (plan §236: "App shutdown
// cancels its registered turns") — and gives their relays a few seconds to
// record the cancellation. A turn still unfinished after that is caught by
// the next startup's reconcile.
func (sup *chatSupervisor) stopAll() {
	sup.mu.Lock()
	sup.stopping = true
	clear(sup.afterConversation)
	handles := make([]*chatTurnHandle, 0, len(sup.turns))
	for _, h := range sup.turns {
		handles = append(handles, h)
	}
	sup.mu.Unlock()
	for _, h := range handles {
		if h.host {
			h.detach() // the host's turn outlives the app: stop following it, not the turn
			continue
		}
		h.requestStop()
	}
	done := make(chan struct{})
	go func() { sup.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}
