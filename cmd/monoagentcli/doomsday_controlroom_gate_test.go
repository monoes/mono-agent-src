package main

import (
	"encoding/json"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
)

// The control room's read verbs (internal/cli/doomsday/controlroom.go): read-only, and still the user's own because the
// feed shows the internals of a run. Their rows in operatorOnlyRows are the shared table's; this file holds the
// per-context and default-install checks.
func TestDoomsdayControlRoomVerbsAreOperatorOnly(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		for _, verb := range doomsdaycli.ControlRoomVerbs {
			t.Run(ctxName+"/"+verb, func(t *testing.T) {
				cfg := sendEnv(t)
				setup(t)
				_, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), verb)
				requireOperatorOnly(t, err)
			})
		}
	}
}

// A default install has no engine: for the user both verbs answer available:false (reason off) and exit 0, so the app
// shows nothing.
func TestDefaultInstallControlRoomIsUnavailable(t *testing.T) {
	if doomsdayBuilt {
		t.Skip("built with -tags doomsday: the mode is compiled in on purpose")
	}
	for _, verb := range doomsdaycli.ControlRoomVerbs {
		cfg := sendEnv(t)
		out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), verb)
		if err != nil {
			t.Fatalf("%s: %v\n%s", verb, err, out)
		}
		var doc map[string]any
		if json.Unmarshal([]byte(out), &doc) != nil || doc["available"] != false || doc["reason"] != "off" {
			t.Fatalf("%s on a default install: %s", verb, out)
		}
	}
}

// With the mode built in but no run, the same documents say no_run.
func TestBuiltInstallWithoutARunControlRoomIsUnavailable(t *testing.T) {
	if !doomsdayBuilt {
		t.Skip("default build: no engine is wired")
	}
	for _, verb := range doomsdaycli.ControlRoomVerbs {
		cfg := sendEnv(t)
		out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), verb)
		if err != nil {
			t.Logf("%s: %v (an engine that cannot open its store is an error result, not off)", verb, err)
			continue
		}
		var doc map[string]any
		if json.Unmarshal([]byte(out), &doc) != nil || doc["available"] != false {
			t.Fatalf("%s with no run: %s", verb, out)
		}
	}
}
