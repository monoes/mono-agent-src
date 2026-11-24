package workflowedit

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// node add checks --type against the registry the CLI hands in, and a
// deprecated type is refused with its migration hint (exit 3, invalid input).
func TestNodeAddChecksTypeAgainstRegistry(t *testing.T) {
	cfg := newTestDB(t, "")
	if _, err := run(t, cfg, editorDoc, "save"); err != nil {
		t.Fatal(err)
	}
	d := testDeps(cfg)
	d.KnownNodeType = func(typ string) bool { return typ == "core.set" }
	try := func(typ string) error {
		root := &cobra.Command{Use: "workflow", SilenceUsage: true, SilenceErrors: true}
		root.AddCommand(d.NewNodeCmd())
		root.SetArgs([]string{"node", "add", "wf-e", "--type", typ, "--name", "x"})
		return root.Execute()
	}
	if err := try("core.nonesuch"); err == nil || !strings.Contains(err.Error(), "core.nonesuch") {
		t.Fatalf("an unknown node type was accepted: %v", err)
	}
	if err := try("service.openrouter"); err == nil || !strings.Contains(err.Error(), "deprecated") {
		t.Fatalf("a deprecated node type was accepted: %v", err)
	}
	if err := try("core.set"); err != nil {
		t.Fatalf("a known node type was refused: %v", err)
	}
}
