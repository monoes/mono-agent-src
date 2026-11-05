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

// Without a run the finish verbs refuse for the user (the reason is the build's, finishNoRunRefusal) and start nothing.
func TestFinishVerbsWithNoRunRefuse(t *testing.T) {
	cfg := sendEnv(t)
	for verb, rest := range doomsdayFinishVerbs {
		out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), append([]string{verb}, rest...)...)
		if err == nil || !strings.Contains(err.Error(), finishNoRunRefusal) {
			t.Fatalf("%s did not refuse (%q): %v\n%s", verb, finishNoRunRefusal, err, out)
		}
	}
}
