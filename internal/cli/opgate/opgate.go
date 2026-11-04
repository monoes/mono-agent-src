// Package opgate is the one question every operator-only command asks: why is this process not the
// user's own terminal? "" means it is.
//
// Without a registered body the answer is operator.Refusal (the environment markers and the
// ancestry of marker-carrying processes), which is what library use and the tests of other packages
// get. cmd/monoagentcli registers the turn-aware body at process start (agent tokens first, then
// operator.Refusal, then whether the process descends from a coder session's turn), so that every
// command that asks here is refused to a model's shell that carries no marker.
//
// The recovery flavour is for the commands that repair a damaged install (a vault key that no longer
// matches its database, a profile that cannot be removed): they must not lock the operator out when
// the database cannot be read, so for them an unreadable or absent database counts as "no turn
// recorded". A process that descends from a turn recorded in a readable database is refused by both.
package opgate

import (
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
)

// UnplacedPrefix starts the reason of a process the gate could not place (the database or the turn
// registry cannot be read): the refusal is then not about who it is, and a retry or a repair can change it.
// cmd/monoagentcli words the reason with it, and a test there keeps the two in step.
const UnplacedPrefix = "cannot tell whether a coder session's turn runs this command ("

// Unplaced says whether why, a Refusal, is the reason of a process that could not be placed.
func Unplaced(why string) bool { return strings.HasPrefix(why, UnplacedPrefix) }

// body answers for one process; recovery selects the recovery flavour.
type body func(recovery bool) string

var registered body

// recoveryBody is the registered recovery check that can also say "the owning host is busy": a refusal that is
// not an operator-only one. Nil unless cmd/monoagentcli registered it.
var recoveryBody func() (why string, busy error)

// Refusal is why this process may not do what only the user does, or "" for the user.
func Refusal() string {
	if registered != nil {
		return registered(false)
	}
	return operator.Refusal()
}

// RecoveryRefusal is Refusal for a command that repairs an install: an unreadable or absent
// database is "no turn recorded" and does not refuse.
func RecoveryRefusal() string {
	if recoveryBody != nil {
		why, busy := recoveryBody()
		if busy != nil && why == "" {
			why = busy.Error()
		}
		return why
	}
	if registered != nil {
		return registered(true)
	}
	return operator.Refusal()
}

// RecoveryCheck is RecoveryRefusal for a command that tells a busy host apart: busy is non-nil when the
// database of a live coder host is locked past the wait and no turn was found elsewhere, which the command
// hands back as it is (a plain error: wait and retry), not as an operator-only refusal. why is as RecoveryRefusal's.
func RecoveryCheck() (why string, busy error) {
	if recoveryBody != nil {
		return recoveryBody()
	}
	return RecoveryRefusal(), nil
}

// RegisterRecovery installs the recovery check that reports a busy host. Same caller rule as Register.
func RegisterRecovery(f func() (string, error)) {
	pc, _, _, ok := runtime.Caller(1)
	if fn := runtime.FuncForPC(pc); !ok || fn == nil || !(strings.HasPrefix(fn.Name(), "main.") || strings.HasPrefix(fn.Name(), "github.com/monoes/mono-agent/cmd/monoagentcli.")) {
		panic("opgate.RegisterRecovery may only be called from cmd/monoagentcli")
	}
	recoveryBody = f
}

// Register installs the turn-aware body. Only package main of cmd/monoagentcli may call it (a
// guard against stray registrations, not a security boundary); anything else panics.
func Register(f func(recovery bool) string) {
	pc, _, _, ok := runtime.Caller(1)
	// "main." in the binary, the import path in the test binary of that package
	if fn := runtime.FuncForPC(pc); !ok || fn == nil || !(strings.HasPrefix(fn.Name(), "main.") || strings.HasPrefix(fn.Name(), "github.com/monoes/mono-agent/cmd/monoagentcli.")) {
		panic("opgate.Register may only be called from cmd/monoagentcli")
	}
	registered = f
}

// SetForTest replaces the registered body for the rest of the test; nil restores the default. It
// panics outside a test binary.
func SetForTest(t testing.TB, f func(recovery bool) string) {
	t.Helper()
	if !testing.Testing() {
		panic("opgate.SetForTest is a test seam and may only be used from a test binary")
	}
	prev, prevRecovery := registered, recoveryBody
	registered, recoveryBody = f, nil // a stand-in body answers for both flavours
	t.Cleanup(func() { registered, recoveryBody = prev, prevRecovery })
}
