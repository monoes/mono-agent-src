package nodes

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Legacy ~/.monoagent/actions/<dir> named after a built-in namespace
// registers nothing: "image/resize" used to panic on the duplicate
// "image.resize", and "trigger/send_dm" would pass as a trigger.
func TestRegisterBrowserNodesSkipsBuiltinNamespaces(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	action.SetDefSource(nil)
	for _, rel := range []string{"image/resize.json", "trigger/send_dm.json", "Core/set.json"} {
		p := filepath.Join(home, ".monoagent", "actions", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{"sideEffects":"write","steps":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	builtin := func() workflow.NodeExecutor { return nil }
	r := workflow.NewNodeTypeRegistry()
	r.Register("image.resize", builtin)

	RegisterBrowserNodes(r) // must not panic

	for _, nt := range []string{"trigger.send_dm", "core.set", "Core.set"} {
		if r.Has(nt) {
			t.Errorf("%s registered from a legacy actions dir", nt)
		}
	}
	if f, _ := r.Get("image.resize"); f == nil || f() != nil {
		t.Error("the built-in image.resize was replaced")
	}
}
