package nodes

import (
	"errors"
	"flag"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/bot"
	"github.com/monoes/mono-agent/internal/workflow"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/browser_node_types_*.golden")

// goldenFile is per build: the social gate changes which node types exist.
func goldenFile() string {
	if bot.PlatformCompiledIn("instagram") {
		return filepath.Join("testdata", "browser_node_types_social.golden")
	}
	return filepath.Join("testdata", "browser_node_types_nosocial.golden")
}

func browserNodeTypes() []string {
	r := workflow.NewNodeTypeRegistry()
	RegisterBrowserNodes(r)
	types := r.Types()
	sort.Strings(types)
	return types
}

func readGolden(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(goldenFile())
	if err != nil {
		t.Fatalf("read golden: %v (run with -update-golden to create it)", err)
	}
	return strings.Fields(string(raw))
}

func diffTypes(t *testing.T, label string, want, got []string) {
	t.Helper()
	w := map[string]bool{}
	for _, s := range want {
		w[s] = true
	}
	g := map[string]bool{}
	for _, s := range got {
		g[s] = true
	}
	for _, s := range want {
		if !g[s] {
			t.Errorf("%s: node type %q missing", label, s)
		}
	}
	for _, s := range got {
		if !w[s] {
			t.Errorf("%s: unexpected node type %q", label, s)
		}
	}
}

// TestBrowserNodeTypes_LegacyGolden: with no automation registry installed,
// the registered browser node types equal the pre-package list (captured
// from the legacy loader on origin/master behaviour).
func TestBrowserNodeTypes_LegacyGolden(t *testing.T) {
	testhome.SetHome(t, t.TempDir()) // no ~/.monoagent/actions leaking in
	action.SetDefSource(nil)
	got := browserNodeTypes()
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenFile(), []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	diffTypes(t, "legacy", readGolden(t), got)
}

// TestBrowserNodeTypes_RegistryGolden: after seeding the built-in packages
// into a fresh registry and switching the loader to it, the same node types
// are registered — existing workflows keep resolving.
func TestBrowserNodeTypes_RegistryGolden(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	if _, err := automations.Tree().ReadDir("automations"); err != nil {
		t.Fatalf("embedded automations: %v", err)
	}
	reg, err := BootAutomations(filepath.Join(home, ".monoagent"))
	t.Cleanup(func() { action.SetDefSource(nil) })
	if errors.Is(err, automation.ErrNotImplemented) {
		t.Skip("automation registry not implemented yet")
	}
	if err != nil || reg == nil {
		t.Fatalf("BootAutomations: %v", err)
	}
	diffTypes(t, "registry", readGolden(t), browserNodeTypes())
}
