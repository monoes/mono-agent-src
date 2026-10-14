package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// TestWorkflowBundleAdapterWiring pins what workflowBundleDeps hands internal/cli/workflowbundle:
// errInvalidInput's exit 3 and the real registry under $HOME. The bundle round trips through
// `workflow export`/`import` are the other workflow_bundle_*_test.go files; the confirmation
// sentinel is pinned by TestWorkflowBundleDiffersSameVersion.
func TestWorkflowBundleAdapterWiring(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := parseAutomationDomains([]string{"a"}); exitCodeFor(err) != 3 {
		t.Fatalf("bad --automation-domains: exit %d (%v), want 3", exitCodeFor(err), err)
	}
	file := workflow.WorkflowFile{Name: "w", Nodes: []workflow.WorkflowFileNode{{ID: "t", Type: "trigger.manual"}}}
	if _, err := bundleWorkflowAutomations(file, bundleOptions{domains: map[string][]string{"typo": {"x.com"}}}); exitCodeFor(err) != 3 {
		t.Fatalf("unknown --automation-domains id: exit %d (%v), want 3", exitCodeFor(err), err)
	}
	b, err := bundleWorkflowAutomations(file, bundleOptions{skipShipped: true})
	if err != nil || len(b.Automations) != 0 || len(b.Nodes) != 1 {
		t.Fatalf("bundle = %+v, %v", b, err)
	}
	if items := handleBundledAutomations([]byte(`{"automations":{"zz-not-installed":{"version":"1","sha256":"00","mpkg":""}}}`), bundleImportOptions{yes: true}); len(items) != 1 || items[0].Status != "failed" {
		t.Fatalf("items = %+v", items)
	}
}
