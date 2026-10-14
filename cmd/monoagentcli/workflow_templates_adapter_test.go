package main

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// TestWorkflowTemplatesAdapterWiring pins what workflowTemplatesDeps hands
// internal/cli/workflowtemplates: `templates run` from main under `templates`, the real hybrid
// store behind `search`, and the real node registry behind `templates use` (every bundled
// template's node types are installed in a default build). `templates use` persisting through
// createOrOverwriteWorkflowAtomically is TestWorkflowTemplatesUse_PersistsSuccessfully.
func TestWorkflowTemplatesAdapterWiring(t *testing.T) {
	_, cfg := persistTestEnv(t)
	if c, _, err := newWorkflowTemplatesCmd(cfg).Find([]string{"run"}); err != nil || c.Short != newWorkflowTemplatesRunCmd(cfg).Short {
		t.Fatalf("templates run: %v", err)
	}
	saved := saveDoc(t, cfg, editorDoc)
	out, err := runWorkflowCmdIn(t, cfg, "", "search", "from-editor")
	if err != nil || !strings.Contains(out, `"source":"workflow","id":"`+saved.ID+`"`) {
		t.Fatalf("search = %q, %v", out, err)
	}
	for _, tm := range workflow.ListTemplates() {
		tmpl, _ := workflow.GetTemplate(tm.ID)
		if err := workflowTemplatesDeps(cfg).CheckNodeTypes(tm.ID, tmpl); err != nil {
			t.Errorf("template %s: %v", tm.ID, err)
		}
	}
	missing := workflow.WorkflowFile{Nodes: []workflow.WorkflowFileNode{{ID: "x", Type: "zz-none.act"}}}
	if err := workflowTemplatesDeps(cfg).CheckNodeTypes("t", missing); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "library install automation zz-none") {
		t.Fatalf("missing node type: exit %d (%v)", exitCodeFor(err), err)
	}
}
