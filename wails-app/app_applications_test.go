// wails-app/app_applications_test.go
package main

import (
	"context"
	"strings"
	"testing"
)

func TestApplicationBindingsRejectFlagLookingArguments(t *testing.T) {
	a := newTestApp(t)
	a.ctx = context.Background()
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "echo {}\n")) // never reached for the bad calls
	checks := map[string]func() error{
		"get id":       func() error { _, e := a.GetApplication("--profile"); return e },
		"status id":    func() error { return a.SetApplicationStatus("-x", "cancelled", "") },
		"status value": func() error { return a.SetApplicationStatus("id1", "--foo", "") },
		"eval id":      func() error { _, e := a.EvaluateApplication("--x", "claude"); return e },
		"eval runtime": func() error { _, e := a.EvaluateApplication("id1", "--foo"); return e },
		"send id":      func() error { return a.SendApplication("-x", "") },
		"apply id":     func() error { _, e := a.ApplyToApplication("-x", nil, nil); return e },
	}
	for name, fn := range checks {
		err := fn()
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Errorf("%s: want an invalid-argument error, got %v", name, err)
		}
	}
}

// TestSetApplicationStatus_RejectsApplied covers the invariant that
// automation must never be able to mark an application "applied" -- only
// the explicit, human-triggered `application send` action may do that (see
// cmd/monoagentcli/application_apply.go's `send` command and
// internal/nodes/applications/set_status.go's SetStatusNode). Every
// exported *App method is reachable from the renderer's JS namespace, so
// this Go-level method must itself reject "applied" as defense-in-depth on
// top of the CLI-level guard in `application status ... set`, in case a
// future direct code path calls the CLI differently than the current
// Applications.jsx (which only ever passes "cancelled" here today).
//
// No CLI binary needs to exist on PATH for this test: the check must
// happen before runMonoCLI (and therefore findMonoAgentCLI) is ever
// invoked. If this test somehow got past the guard, it would fail on
// "monoagentcli executable not found" instead of the expected message,
// which would also correctly flag a regression.
func TestSetApplicationStatus_RejectsApplied(t *testing.T) {
	a := newTestApp(t)

	if err := a.SetApplicationStatus("some-id", "applied", ""); err == nil {
		t.Fatal("expected error setting status to \"applied\", got nil")
	} else if !strings.Contains(err.Error(), "SendApplication") {
		t.Fatalf("expected error to point at SendApplication, got: %v", err)
	}
}

// TestSetApplicationStatus_RejectsAppliedCaseInsensitive covers casing
// variants of "applied" (e.g. a future caller passing "Applied" or
// "APPLIED") -- the guard must not be bypassable by casing alone.
func TestSetApplicationStatus_RejectsAppliedCaseInsensitive(t *testing.T) {
	a := newTestApp(t)

	for _, variant := range []string{"Applied", "APPLIED", "aPpLiEd"} {
		if err := a.SetApplicationStatus("some-id", variant, ""); err == nil {
			t.Fatalf("expected error setting status to %q, got nil", variant)
		}
	}
}

// TestSetApplicationStatus_AllowsOtherStatuses covers that the guard is
// scoped to "applied" only -- other statuses must still reach the CLI
// (and fail there, since no monoagentcli binary or real application id is
// set up here; that failure just proves the call was not short-circuited
// by the "applied" guard).
func TestSetApplicationStatus_AllowsOtherStatuses(t *testing.T) {
	a := newTestApp(t)
	a.ctx = context.Background() // runMonoCLI needs a non-nil context to shell out

	err := a.SetApplicationStatus("some-id", "cancelled", "")
	if err == nil {
		t.Fatal("expected an error (no monoagentcli binary / no such application in this test setup), got nil")
	}
	if strings.Contains(err.Error(), "SendApplication") {
		t.Fatalf("\"cancelled\" must not be rejected by the \"applied\" guard, got: %v", err)
	}
}
