package agentbox

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Mode is which sandbox profile a turn runs under.
type Mode string

const (
	// Compat keeps the filesystem writable and hides or protects a list of paths:
	// best effort, not a boundary.
	Compat Mode = "compat"
	// Strict makes the home directory, /tmp and /run empty and binds back only what
	// the turn needs.
	Strict Mode = "strict"
)

// Codes of the errors this package returns.
const (
	CodeUnsupportedOS = "box_unsupported_os"
	CodeUnavailable   = "box_unavailable"
	CodeCanary        = "box_canary_failed"
	CodeBadPath       = "box_path_refused"
	CodeUntrusted     = "box_untrusted_launcher"
)

// Error is a typed box error: Code is stable, Msg is for people.
type Error struct{ Code, Msg string }

func (e *Error) Error() string { return e.Msg }

// JSONErrorFields lets the CLI put the code in its --json error document.
func (e *Error) JSONErrorFields() map[string]any { return map[string]any{"code": e.Code} }

func errf(code, format string, a ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// IsCode reports whether err is a *Error with that code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Box describes the sandbox one turn runs in.
type Box struct {
	Mode Mode
	// Cwd is the turn's working directory: bound read-write in strict mode.
	Cwd string
	// AllowRW and AllowRO are extra paths bound back in strict mode ("~/x" or absolute).
	AllowRW, AllowRO []string
	// Runtime is the agent runtime id; its install location is bound read-only.
	Runtime string
	// Home overrides $HOME (tests). StateDir is mono-agent's state directory; it defaults
	// to Home/.monoagent and is what the canary checks is hidden.
	Home, StateDir string
	// Protect are further files or directories that must be unreadable inside the box: the
	// database actually in use (--db-path) and the like. Like StateDir they are masked
	// wherever they are, symlink or not, and refused as a working directory or allowed path.
	Protect []string
	// Binaries are further executables the turn starts (monomind, monoagentcli): their
	// install locations are bound read-only.
	Binaries []string
	// ReadOnly are the regular files the caller itself wrote for the turn (its prompt files),
	// bound read-only. Attach are files supplied for the turn: each must be a canonical
	// regular file (no symlink, no directory) that is not, or under, a credential, startup or
	// state location.
	ReadOnly, Attach []string
	// Bwrap is the absolute path of the bubblewrap launcher. Pin sets it once per turn so the
	// canary and the turn run the same, checked executable; empty means Wrap resolves it.
	Bwrap string
	// PassEnv names variables passed on besides the allowlist and MONOAGENT_*.
	PassEnv []string
}

// Plan is the command to start instead of the turn's own, and what could not be resolved.
type Plan struct {
	Argv []string
	// Notes lists what the box could not resolve (a binary not found), for the caller to show.
	Notes []string
	// Files are the descriptors the bind arguments refer to; Command passes them to the
	// launcher and Close releases them.
	Files []*os.File
}

// Close releases the descriptors of the plan.
func (p Plan) Close() {
	for _, f := range p.Files {
		f.Close()
	}
}

// Command is the command to start for the plan. The launcher gets a minimal environment of
// its own: bubblewrap's init process keeps the environment it was started with readable in
// /proc/1/environ, so the caller's ambient environment must not be inherited. What the
// payload may see travels as --setenv arguments.
func (p Plan) Command() *exec.Cmd {
	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.ExtraFiles = p.Files
	return cmd
}
