package workflow_test

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/workflow"
)

// TestBundledTemplates_NodeTypesRegistered checks every template node type
// against the default build's node registry. Triggers are handled by the
// engine itself, and gemini.* nodes come from the Gemini automation, which
// registers once installed — both are outside the static registry.
func TestBundledTemplates_NodeTypesRegistered(t *testing.T) {
	registry := noderegistry.Build(nil)
	for _, meta := range workflow.ListTemplates() {
		wf, ok := workflow.GetTemplate(meta.ID)
		if !ok {
			t.Fatalf("template %q listed but not found", meta.ID)
		}
		for _, n := range wf.Nodes {
			if strings.HasPrefix(n.Type, "trigger.") || strings.HasPrefix(n.Type, "gemini.") {
				continue
			}
			if !registry.Has(n.Type) {
				t.Errorf("%s: node %s has unregistered type %q", meta.ID, n.ID, n.Type)
			}
		}
	}
}
