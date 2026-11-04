package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
)

// The resource governor's one verb, `doomsday governor status`, exists only in a build with the mode, is read-only by
// construction, and is a read verb behind the sign-in like the other read verbs: gated, NOT open like Stop. The group is
// operator-only for client mode like the rest of `doomsday` (the client-mode table classifies the group).

func governorGroup(t *testing.T) *cobra.Command {
	t.Helper()
	root := newRootCmd()
	root.InitDefaultHelpCmd()
	applyClassification(root)
	grp, _, err := root.Find([]string{"doomsday", "governor"})
	if err != nil || grp == nil || grp.Name() != "governor" {
		return nil
	}
	return grp
}

func TestGovernorVerbExistsOnlyInABuildWithTheMode(t *testing.T) {
	grp := governorGroup(t)
	if !doomsdayBuilt {
		if grp != nil {
			t.Fatal("a default build has the governor verb")
		}
		return
	}
	if grp == nil {
		t.Fatal("a build with the mode lacks `doomsday governor`")
	}
	if !grp.Hidden {
		t.Error("the governor group is listed in help")
	}
	var verbs []string
	for _, c := range grp.Commands() {
		verbs = append(verbs, c.Name())
		if c.Annotations[doomsdaycli.AnnotationReadOnly] != "true" {
			t.Errorf("governor %s is not marked read-only", c.Name())
		}
		if got := commandClass(c); got != classGated {
			t.Errorf("governor %s has account class %q: a read verb waits for the sign-in and is not open like Stop", c.Name(), got)
		}
	}
	if strings.Join(verbs, ",") != "status" {
		t.Fatalf("governor verbs changed: %v: classify the new verb and its gate", verbs)
	}
}

// Nothing of the governor takes an argument that names a run, a limit or a queue row: the only flag is --generation, a read.
func TestGovernorStatusTakesNoWriteArguments(t *testing.T) {
	grp := governorGroup(t)
	if grp == nil {
		t.Skip("default build: no governor verb")
	}
	st, _, _ := grp.Find([]string{"status"})
	var flags []string
	st.Flags().VisitAll(func(f *pflag.Flag) { flags = append(flags, f.Name) })
	if strings.Join(flags, ",") != "generation" {
		t.Fatalf("status flags: %v", flags)
	}
}

// On a fresh database with no run the verb says off and exits cleanly.
func TestGovernorStatusSaysOffWithNoRun(t *testing.T) {
	if !doomsdayBuilt {
		t.Skip("default build")
	}
	for _, asJSON := range []bool{true, false} {
		cfg := sendEnv(t)
		cfg.JSONOutput = asJSON
		out, err := runSendCLI(t, cfg, newDoomsdayCmd(cfg), "governor", "status")
		if err != nil || !strings.Contains(out, "off") {
			t.Fatalf("json %v: %v %q", asJSON, err, out)
		}
	}
}
