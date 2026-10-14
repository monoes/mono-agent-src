package workflow

import (
	"strings"
	"testing"
)

// TestGetTemplate_DoesNotAliasSharedCache is a regression test: GetTemplate
// must return an independent copy of the cached WorkflowFile, not one that
// aliases the package-level templateFiles map's nested Config maps. Real call
// sites (wails-app's CreateWorkflowFromTemplate, the CLI's instantiateTemplate)
// mutate a node's Config in place (e.g. config["profile_id"] = callerProfile)
// to stamp their own values before saving — if that mutation reaches the
// shared cache, it permanently corrupts the template for every future
// caller/profile until process restart, and races under concurrent use.
func TestGetTemplate_DoesNotAliasSharedCache(t *testing.T) {
	const templateID = "gemimg"

	first, ok := GetTemplate(templateID)
	if !ok {
		t.Fatalf("template %q not found", templateID)
	}
	if len(first.Nodes) == 0 {
		t.Fatalf("template %q has no nodes", templateID)
	}

	// Mutate the first copy's node Config exactly like a real instantiation
	// call site would (stamping a caller-specific profile_id).
	if first.Nodes[0].Config == nil {
		first.Nodes[0].Config = map[string]interface{}{}
	}
	first.Nodes[0].Config["profile_id"] = "caller-a-profile"

	second, ok := GetTemplate(templateID)
	if !ok {
		t.Fatalf("template %q not found on second fetch", templateID)
	}
	if got, exists := second.Nodes[0].Config["profile_id"]; exists {
		t.Errorf("second GetTemplate call saw mutation from first caller: Config[%q] = %v, want key absent — "+
			"GetTemplate is aliasing the shared cached map instead of returning a deep copy", "profile_id", got)
	}
}

// A saved copy of a template gets its summary; templates show and list keep
// the CLI-oriented description.
func TestSavedTemplateDescription(t *testing.T) {
	for _, tmpl := range ListTemplates() {
		want := tmpl.Summary
		if want == "" {
			want = tmpl.Description
		}
		if got := SavedTemplateDescription(tmpl.ID); got != want {
			t.Errorf("%s: saved description = %q, want %q", tmpl.ID, got, want)
		}
	}
	file, _ := GetTemplate("hello")
	if !strings.Contains(file.Description, "templates run hello") || strings.Contains(SavedTemplateDescription("hello"), "templates run") {
		t.Errorf("hello: description %q, saved %q", file.Description, SavedTemplateDescription("hello"))
	}
	if got := SavedTemplateDescription("no-such"); got != "" {
		t.Errorf("unknown template = %q", got)
	}
}
