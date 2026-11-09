package nodes

import (
	"encoding/json"
	"flag"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/workflow"
)

var updateForms = flag.Bool("update-forms", false, "rewrite testdata/builtin_forms.golden.json")

// TestBuiltinForms_Golden: every built-in browser node type resolves to
// exactly the form in testdata, both with the legacy loader and with the
// automation registry booted. The golden started as origin/master's forms;
// change it only on purpose (-update-forms) and review the diff.
func TestBuiltinForms_Golden(t *testing.T) {
	path := filepath.Join("testdata", "builtin_forms.golden.json")
	if *updateForms {
		testhome.SetHome(t, t.TempDir())
		action.SetDefSource(nil)
		out := map[string]json.RawMessage{}
		for _, nt := range browserNodeTypes() {
			s, err := workflow.LoadDefaultSchema(nt)
			if err != nil {
				t.Fatal(err)
			}
			out[nt], _ = json.Marshal(s)
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]json.RawMessage
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}

	check := func(label string) {
		t.Helper()
		for _, nt := range browserNodeTypes() {
			want, ok := golden[nt]
			if !ok {
				t.Errorf("%s: no golden form for %s", label, nt)
				continue
			}
			s, err := workflow.LoadDefaultSchema(nt)
			if err != nil {
				t.Errorf("%s: %s: %v", label, nt, err)
				continue
			}
			got, _ := json.Marshal(s)
			if !jsonEqual(t, got, want) {
				t.Errorf("%s: %s form changed\n got: %s\nwant: %s", label, nt, got, want)
			}
		}
	}

	home := t.TempDir()
	testhome.SetHome(t, home)
	action.SetDefSource(nil)
	check("legacy")

	if _, err := BootAutomations(filepath.Join(home, ".monoagent")); err != nil {
		t.Fatalf("BootAutomations: %v", err)
	}
	t.Cleanup(func() { action.SetDefSource(nil) })
	check("registry")
}

func jsonEqual(t *testing.T, a, b []byte) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}
