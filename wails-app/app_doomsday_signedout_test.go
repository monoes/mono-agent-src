//go:build !windows

package main

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// Of all doomsday bindings exactly Stop is in openBindings. The guard reads the table itself, so a second name fails.
func TestOnlyDoomsdayStopRunsSignedOut(t *testing.T) {
	var open []string
	for name := range openBindings {
		if strings.HasPrefix(name, "Doomsday") {
			open = append(open, name)
		}
	}
	sort.Strings(open)
	if strings.Join(open, ",") != "DoomsdayStop" {
		t.Fatalf("doomsday bindings open signed out: %v, want only DoomsdayStop", open)
	}
	// Every doomsday binding but Stop refuses signed out, by name (not only through the generic walk).
	accounttest.Install(t, accounttest.LockedNoLogin)
	a := newTestApp(t)
	typ := reflect.TypeOf(a)
	var gated []string
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		if !strings.HasPrefix(m.Name, "Doomsday") || m.Name == "DoomsdayStop" {
			continue
		}
		gated = append(gated, m.Name)
		before := gateRefusals.Load()
		callZero(t, a, m)
		if gateRefusals.Load()-before != 1 {
			t.Errorf("%s ran signed out", m.Name)
		}
	}
	// The start form (OpenEntry, Render, Start) is gated like the reads: starting a run needs the sign-in, only Stop
	// does not. The list is exact, so a further binding that skips the gate cannot hide in a count.
	if strings.Join(gated, ",") != "DoomsdayFeed,DoomsdayOpenEntry,DoomsdayOverview,DoomsdayProbe,DoomsdayRender,DoomsdayStart,DoomsdayStatus" {
		t.Fatalf("gated doomsday bindings: %v", gated)
	}
}

// Signed out, with no network and no session, Stop reaches the CLI with exactly `doomsday stop` and never asks the
// account gate.
func TestDoomsdayStopWorksSignedOut(t *testing.T) {
	log, _ := taskCLIFake(t, "stopped\n", 0)
	a := newTaskTestApp(t)
	accounttest.Install(t, accounttest.LockedNoLogin)
	if account.Require(t.Context()) == nil {
		t.Fatal("test setup: the session allows work")
	}
	before := gateRefusals.Load()
	var d struct {
		OK, Stopped   bool
		Code, Output  string
		LoginRequired bool `json:"login_required"`
	}
	if json.Unmarshal([]byte(a.DoomsdayStop()), &d) != nil || d.LoginRequired || !d.OK || !d.Stopped {
		t.Fatalf("stop doc %+v", d)
	}
	if gateRefusals.Load() != before {
		t.Fatal("Stop consulted the account gate")
	}
	if got := loggedArgs(t, log); len(got) != 1 || got[0] != "--profile|work|--json|doomsday|stop|" {
		t.Fatalf("argv %q", got)
	}
}

// Signed out, the operator gate still refuses an agent's process before the CLI runs.
func TestDoomsdayStopSignedOutStillNeedsTheOperator(t *testing.T) {
	opgate.SetForTest(t, func(bool) string { return "an agent's shell" })
	log, _ := taskCLIFake(t, "stopped\n", 0)
	a := newTaskTestApp(t)
	accounttest.Install(t, accounttest.LockedNoLogin)
	var d struct {
		OK   bool
		Code string
	}
	if json.Unmarshal([]byte(a.DoomsdayStop()), &d) != nil || d.OK || d.Code != "operator_only" {
		t.Fatalf("stop doc %+v", d)
	}
	if callCount(t, log) != 0 {
		t.Fatal("the CLI ran")
	}
}
