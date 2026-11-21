package workflow

import (
	"strings"
	"testing"
)

// starterTemplateIDs are the templates the onboarding's "Your first run" step
// runs for a brand-new user (wails-app/frontend/src/onboarding/starters.js).
var starterTemplateIDs = []string{"starter_page_read", "starter_summarise_page"}

// TestStarterTemplatesLoadAndValidate: each starter is listed, has a plain
// summary and no order (the onboarding picks it by id, the gallery lists it
// by name), and passes the checks every bundled template gets — the graph
// and each node against its schema, as hello does.
func TestStarterTemplatesLoadAndValidate(t *testing.T) {
	listed := map[string]Template{}
	for _, tmpl := range ListTemplates() {
		listed[tmpl.ID] = tmpl
	}
	for _, id := range starterTemplateIDs {
		t.Run(id, func(t *testing.T) {
			tmpl, ok := listed[id]
			if !ok {
				t.Fatalf("template %q not in ListTemplates()", id)
			}
			if strings.TrimSpace(tmpl.Summary) == "" {
				t.Errorf("summary is empty")
			}
			if order := templateMetas[id].Order; order != 0 {
				t.Errorf("order = %d, want none", order)
			}
			if len(tmpl.Inputs) != 0 {
				t.Errorf("a starter must run without input, reads %v", tmpl.Inputs)
			}
			wf, ok := GetTemplate(id)
			if !ok {
				t.Fatalf("GetTemplate(%q) not found", id)
			}
			if wf.IsActive {
				t.Errorf("templates must start inactive")
			}
			checkTemplateGraph(t, wf)
			for _, n := range wf.Nodes {
				checkTemplateNode(t, n)
				if _, set := n.Config["credential_id"]; set {
					t.Errorf("node %s: a starter needs no credential", n.ID)
				}
				if rt, ok := n.Config["runtime"].(string); ok && rt != "auto" {
					t.Errorf("node %s: runtime = %q, want auto (a new user may have any runtime)", n.ID, rt)
				}
			}
		})
	}
}
