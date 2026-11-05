package main

import (
	"sort"
	"strings"
	"testing"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
)

// The strategy verbs (internal/cli/doomsday/strategyverbs.go) are read-only by construction: this is their row in the
// operator gate tables (kept in this file, not in the shared table). The group is operator-only for client mode like the
// rest of `doomsday` (the classification test covers it); none of the verbs writes or acts, and none takes an argument that
// names a run, a charter, an approval or a finish.

var doomsdayStrategyVerbs = []string{"directions", "epochs", "status"}

func strategyGroupOf(t *testing.T) (verbs []string, byName map[string]bool) {
	t.Helper()
	cfg := sendEnv(t)
	grp := newDoomsdayCmd(cfg)
	byName = map[string]bool{}
	for _, c := range grp.Commands() {
		if c.Name() != "strategy" {
			continue
		}
		if !c.Hidden {
			t.Error("the strategy group is listed in help")
		}
		for _, v := range c.Commands() {
			verbs = append(verbs, v.Name())
			byName[v.Name()] = v.Annotations[doomsdaycli.AnnotationReadOnly] == "true"
		}
	}
	sort.Strings(verbs)
	return verbs, byName
}

func TestDoomsdayStrategyVerbsAreExactlyTheThreeReadOnlyOnes(t *testing.T) {
	verbs, ro := strategyGroupOf(t)
	if strings.Join(verbs, ",") != strings.Join(doomsdayStrategyVerbs, ",") {
		t.Fatalf("strategy verbs changed: %v: classify the new verb and its gate", verbs)
	}
	for _, v := range verbs {
		if !ro[v] {
			t.Errorf("strategy %s is not marked read-only", v)
		}
	}
	for _, v := range doomsdaycli.StrategyVerbs {
		found := false
		for _, w := range doomsdayStrategyVerbs {
			found = found || v == w
		}
		if !found {
			t.Errorf("StrategyVerbs lists %q, which this table does not classify", v)
		}
	}
}

// With the engine but no run recorded (a fresh database),
// every strategy verb says the layer is off, exits cleanly and starts nothing.
func TestDoomsdayStrategyVerbsSayOffWhenNothingIsRunning(t *testing.T) {
	for _, verb := range doomsdayStrategyVerbs {
		for _, asJSON := range []bool{true, false} {
			cfg := sendEnv(t)
			cfg.JSONOutput = asJSON
			out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), "strategy", verb)
			if err != nil {
				t.Fatalf("%s: %v\n%s", verb, err, out)
			}
			if !strings.Contains(out, "off") || strings.Contains(strings.ToLower(out), "started") {
				t.Fatalf("%s (json %v) did not say off: %q", verb, asJSON, out)
			}
		}
	}
}
