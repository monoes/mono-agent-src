package apipkg

import (
	"regexp"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// TestFieldOwnDependsOnNarrowsVisibleFor: a package field's own depends_on
// joins every visible_for entry, so a legacy-only field shows (and is
// checked) only while its toggle is on.
func TestFieldOwnDependsOnNarrowsVisibleFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Reload()
	t.Cleanup(Reload)
	s, err := workflow.LoadDefaultSchema("app.openai_api")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range s.Fields {
		if f.Label != "Legacy model id" && !strings.HasPrefix(f.Key, "legacy") {
			continue
		}
		n++
		if len(f.VisibleFor) == 0 {
			t.Errorf("%s has no visible_for", f.Key)
		}
		for _, vf := range f.VisibleFor {
			if vf["legacyImageModel"] != "true" && vf["legacyEditModel"] != "true" && vf["legacyModeration"] != "true" {
				t.Errorf("%s shows without its legacy toggle on: %v", f.Key, f.VisibleFor)
			}
		}
	}
	if n != 5 {
		t.Errorf("checked %d legacy fields, want 5", n)
	}
}

// TestFieldLabelsNameNoN8nVersions: labels are what the form shows; which
// n8n version a field came from is importer plumbing.
func TestFieldLabelsNameNoN8nVersions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	Reload()
	t.Cleanup(Reload)
	re := regexp.MustCompile(`(?i)n8n`)
	for _, p := range packages() {
		for _, o := range p.Operations {
			for _, f := range o.Fields {
				if re.MatchString(f.Label) {
					t.Errorf("%s %s.%s: field %q label %q mentions n8n", p.ID, o.Resource, o.Operation, f.Key, f.Label)
				}
			}
		}
	}
}
