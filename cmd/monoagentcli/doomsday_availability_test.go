package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// availEngine is an engine with a chosen containment answer and a chosen run.
type availEngine struct {
	doomsdaycli.Off
	containment error
	status      doomsdaycli.Status
}

func (a availEngine) Containment() error { return a.containment }
func (a availEngine) Status(context.Context) (doomsdaycli.Status, error) {
	return a.status, nil
}

// The chat's mode selector asks coderhost.OpenModeAvailability. Building the chat commands wires it to the real probe
// of the engine, so the reason a person sees is the probe's (a run in this profile, a failed containment step), not
// the stub "not available yet".
func TestChatDepsWireTheOpenModeAvailabilityToTheProbe(t *testing.T) {
	oldEngine := doomsdayEngine
	t.Cleanup(func() { doomsdayEngine = oldEngine })
	t.Cleanup(coderhost.SetOpenModeReady(true))
	t.Cleanup(coderhost.SetOpenModeProbe(nil))
	cfg := sendEnv(t)
	for name, c := range map[string]struct {
		e      availEngine
		ok     bool
		reason string
	}{
		"available":    {availEngine{}, true, ""},
		"a run exists": {availEngine{status: doomsdaycli.Status{Active: true, RunID: "default/2"}}, false, "default/2"},
		"a step fails": {availEngine{containment: errors.New("containment unavailable: pidfd_open: nope")}, false, "pidfd_open"},
	} {
		t.Run(name, func(t *testing.T) {
			doomsdayEngine = func(*globalConfig) doomsdaycli.Engine { return c.e }
			_ = chatDeps(cfg)
			ok, reason := coderhost.OpenModeAvailability()
			if ok != c.ok || !strings.Contains(reason, c.reason) || (!ok && strings.Contains(reason, "not available yet")) {
				t.Fatalf("ok=%v reason=%q", ok, reason)
			}
		})
	}
}

// While the boss binding is pending the wired probe is not enough: the mode stays unavailable with that reason.
func TestWiredProbeDoesNotOfferTheModeWhileTheBossBindingIsPending(t *testing.T) {
	oldEngine := doomsdayEngine
	t.Cleanup(func() { doomsdayEngine = oldEngine })
	t.Cleanup(coderhost.SetOpenModeReady(false))
	t.Cleanup(coderhost.SetOpenModeProbe(nil))
	doomsdayEngine = func(*globalConfig) doomsdaycli.Engine { return availEngine{} }
	_ = chatDeps(sendEnv(t))
	if ok, reason := coderhost.OpenModeAvailability(); ok || !strings.Contains(reason, "boss binding pending") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
}
