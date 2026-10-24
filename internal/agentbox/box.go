package agentbox

import (
	"errors"
	"fmt"
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
	// Binaries are further executables the turn starts (monomind, monoagentcli): their
	// install locations are bound read-only.
	Binaries []string
	// ReadOnly are the regular files the caller itself wrote for the turn (its prompt files),
	// bound read-only. Attach are files supplied for the turn: each must be a canonical
	// regular file (no symlink, no directory) that is not, or under, a credential, startup or
	// state location.
	ReadOnly, Attach []string
	// PassEnv names variables passed on besides the allowlist and MONOAGENT_*.
	PassEnv []string
}

// Plan is the command to start instead of the turn's own, and what could not be resolved.
type Plan struct {
	Argv []string
	// Notes lists what the box could not resolve (a binary not found), for the caller to show.
	Notes []string
}
