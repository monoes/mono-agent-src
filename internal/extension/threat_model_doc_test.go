package extension

import (
	"os"
	"strings"
	"testing"
)

// The threat model must not claim a per-origin grant the manifest does not
// have: while the extension asks for <all_urls> up front, the doc says so.
func TestThreatModelMatchesManifestHostAccess(t *testing.T) {
	manifest, err := os.ReadFile("../../chrome-extension/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile("../../docs/security/threat-model.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifest), "optional_host_permissions") {
		t.Skip("the manifest grants per origin; the doc may describe MA-05")
	}
	if strings.Contains(string(doc), "optional_host_permissions") || strings.Contains(string(doc), "explicitly granted origins") {
		t.Error("threat-model.md describes MA-05 per-origin grants, but the manifest declares <all_urls> and has no optional_host_permissions")
	}
}
