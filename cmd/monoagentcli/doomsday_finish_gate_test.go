package main

import (
	"strings"
	"testing"
)

// The finish verbs of the open-ended mode (note 13.6): every one of them runs the criteria of a charter, writes the
// finish ledger or decides a rubric, so every one is the user's own like start and stop. This is their row in the
// operator gate tables (kept in this file, not in the shared table).
var doomsdayFinishVerbs = map[string][]string{
	"finish": {},
	"rubric": {"c1", "approve", "--dossier", "abcdef12"},
}

func TestDoomsdayFinishVerbsAreOperatorOnly(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		for verb, rest := range doomsdayFinishVerbs {
			t.Run(ctxName+"/"+verb, func(t *testing.T) {
				cfg := sendEnv(t)
				setup(t)
				_, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), append([]string{verb}, rest...)...)
				requireOperatorOnly(t, err)
			})
		}
		t.Run(ctxName+"/finish --begin", func(t *testing.T) {
			cfg := sendEnv(t)
			setup(t)
			_, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), "finish", "--begin")
			requireOperatorOnly(t, err)
		})
	}
}

// A default install carries no engine, so the finish verbs refuse as off even for the user, and the group is hidden.
func TestDefaultInstallCannotFinishARun(t *testing.T) {
	if doomsdayBuilt {
		t.Skip("built with -tags doomsday: the mode is compiled in on purpose")
	}
	cfg := sendEnv(t)
	for verb, rest := range doomsdayFinishVerbs {
		out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), append([]string{verb}, rest...)...)
		if err == nil || !strings.Contains(err.Error(), "not available on this install") && !strings.Contains(err.Error(), "only the user") {
			t.Fatalf("%s did not refuse as off: %v\n%s", verb, err, out)
		}
	}
}
