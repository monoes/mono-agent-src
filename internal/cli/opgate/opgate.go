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

// body answers for one process; recovery selects the recovery flavour.
type body func(recovery bool) string

var registered body

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
	if registered != nil {
		return registered(true)
	}
	return operator.Refusal()
}

// Register installs the turn-aware body. Only package main of cmd/monoagentcli may call it (a
// guard against stray registrations, not a security boundary); anything else panics.
func Register(f func(recovery bool) string) {
	pc, _, _, ok := runtime.Caller(1)
	// "main." in the binary, the import path in the test binary of that package
	if fn := runtime.FuncForPC(pc); !ok || fn == nil || !(strings.HasPrefix(fn.Name(), "main.") || strings.HasPrefix(fn.Name(), "github.com/monoes/mono-agent/cmd/monoagentcli.")) {
		panic("opgate.Register may only be called from cmd/monoagentcli")
	}
	install(f)
}

// install makes f the registered body and the turn check operator.ApprovingContext consults, so a
// process that registers no body approves by marker alone. The check is the verdict of Refusal, and
// anything that goes wrong in it (a panic on an unset database path) counts as inside a turn.
func install(f body) {
	registered = f
	if f == nil {
		operator.SetTurnVerdict(nil)
		return
	}
	operator.SetTurnVerdict(func() (why string) {
		defer func() {
			if recover() != nil {
				why = "cannot tell whether a coder session's turn runs this command"
			}
		}()
		return f(false)
	})
}

// SetForTest replaces the registered body for the rest of the test; nil restores the default. It
// panics outside a test binary.
func SetForTest(t testing.TB, f func(recovery bool) string) {
	t.Helper()
	if !testing.Testing() {
		panic("opgate.SetForTest is a test seam and may only be used from a test binary")
	}
	prev := registered
	install(f)
	t.Cleanup(func() { install(prev) })
}
