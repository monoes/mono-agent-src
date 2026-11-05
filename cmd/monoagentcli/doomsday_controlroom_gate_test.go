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

// With no run (a default install has no engine, a build with the mode has no run recorded) both verbs answer
// available:false and exit 0, so the app shows nothing. The reason is the build's (controlRoomNoRunReason).
func TestControlRoomWithNoRunIsUnavailable(t *testing.T) {
	for _, verb := range doomsdaycli.ControlRoomVerbs {
		cfg := sendEnv(t)
		out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), verb)
		if err != nil {
			t.Fatalf("%s: %v\n%s", verb, err, out)
		}
		var doc map[string]any
		if json.Unmarshal([]byte(out), &doc) != nil || doc["available"] != false || doc["reason"] != controlRoomNoRunReason {
			t.Fatalf("%s with no run: %s", verb, out)
		}
	}
}
