package apipkg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// TestExtendedNativeOperationSelects: on every native node a catalog
// package extends with resources, the native operation select shows only
// while no resource is picked, every native operation still validates
// without a resource, and a resource's operations validate with it.
func TestExtendedNativeOperationSelects(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Reload()
	t.Cleanup(Reload)
	n := 0
	for _, p := range packages() {
		if p.Extends == "" || len(p.Resources) == 0 {
			continue
		}
		n++
		s, err := workflow.LoadDefaultSchema(p.Extends)
		if err != nil {
			t.Fatalf("%s: %v", p.Extends, err)
		}
		var native *workflow.NodeSchemaField
		for i := range s.Fields {
			if f := &s.Fields[i]; f.Key == "operation" && f.DependsOn != nil && f.DependsOn.Key == "resource" &&
				len(f.DependsOn.Values) == 1 && f.DependsOn.Values[0] == "" {
				native = f
			}
		}
		if native == nil {
			t.Errorf("%s: no native operation select limited to an empty resource", p.Extends)
			continue
		}
		// Native fields come before the resource select; the always-required
		// ones (no condition, no default) must still be enforced natively.
		pkgKeys := map[string]bool{}
		for _, o := range p.Operations {
			for _, f := range o.Fields {
				pkgKeys[f.Key] = true
			}
		}
		nativeOnly, alwaysRequired := map[string]bool{}, map[string]bool{}
		raw, err := os.ReadFile(filepath.Join("..", "workflow", "schemas", p.Extends+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var file workflow.NodeSchema
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatal(err)
		}
		for _, f := range file.Fields {
			if f.Type == "credential_picker" || f.Key == "credential_id" {
				continue
			}
			if !pkgKeys[f.Key] {
				nativeOnly[f.Key] = true
			}
			if f.Required && f.Default == nil && f.DependsOn == nil && len(f.VisibleFor) == 0 {
				alwaysRequired[f.Key] = true
			}
		}
		check := func(cfg map[string]any) map[string]bool {
			t.Helper()
			w := &workflow.Workflow{Name: "ops", Nodes: []workflow.WorkflowNode{{ID: "n", Type: p.Extends, Name: "N", Config: cfg}}}
			missing := map[string]bool{}
			for _, is := range workflow.ValidateWorkflow(w, workflow.ValidateOptions{}).Issues {
				if is.Field == "operation" || is.Field == "resource" {
					t.Errorf("%s %v: %s %s (%s)", p.Extends, cfg, is.Code, is.Message, is.Hint)
				}
				if is.Code == workflow.IssueMissingRequired {
					missing[is.Field] = true
				}
			}
			return missing
		}
		for _, op := range native.Options {
			missing := check(map[string]any{"operation": op})
			for k := range alwaysRequired {
				if !missing[k] {
					t.Errorf("%s operation %s: required native field %q not enforced", p.Extends, op, k)
				}
			}
		}
		for _, r := range p.Resources {
			for _, op := range p.opNames(r.Value) {
				for k := range check(map[string]any{"resource": r.Value, "operation": op}) {
					if nativeOnly[k] {
						t.Errorf("%s %s.%s: native field %q required on a package op", p.Extends, r.Value, op, k)
					}
				}
			}
		}
	}
	if n < 21 {
		t.Errorf("checked %d extended natives, want at least 21", n)
	}
}

// TestOperationSelectPerResource: every package schema (and every native
// schema a package extends) has, for each resource value, exactly one
// operation select depending on it, and its default is one of its own
// options: a consumer picks the select whose depends_on matches the
// resource and reads that select's default.
func TestOperationSelectPerResource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Reload()
	t.Cleanup(Reload)
	for _, p := range packages() {
		if len(p.Operations) == 0 || len(p.Resources) == 0 {
			continue
		}
		s, err := workflow.LoadDefaultSchema(p.TargetType())
		if err != nil {
			t.Fatalf("%s: %v", p.TargetType(), err)
		}
		var resources []string
		perResource := map[string]int{}
		for _, f := range s.Fields {
			switch {
			case f.Key == "resource" && f.Type == "select":
				resources = f.Options
			case f.Key == "operation":
				if f.DependsOn == nil || f.DependsOn.Key != "resource" || len(f.DependsOn.Values) != 1 {
					t.Errorf("%s: operation select %q does not depend on one resource: %+v", p.TargetType(), f.Label, f.DependsOn)
					continue
				}
				perResource[f.DependsOn.Values[0]]++
				if d, _ := f.Default.(string); !contains(f.Options, d) {
					t.Errorf("%s: %q default %v is not one of its options", p.TargetType(), f.Label, f.Default)
				}
			}
		}
		for _, r := range resources {
			if perResource[r] != 1 {
				t.Errorf("%s: resource %q has %d operation selects, want 1", p.TargetType(), r, perResource[r])
			}
		}
	}
}
