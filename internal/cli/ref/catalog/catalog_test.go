package catalog

import (
	"strings"
	"testing"
)

// Every hand-documented trigger keeps its entry: triggers run through the
// trigger manager, not the node registry, so they have no other source.
// (Registered types without an entry get a schema-generated page; see
// TestRefNodeRendersEveryRegisteredType.)
func TestRefDocsCoverEveryTrigger(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"trigger.manual", "trigger.schedule", "trigger.webhook", "trigger.org", "trigger.sse", "trigger.form", "trigger.local_file", "trigger.error", "trigger.execute_workflow"} {
		if FindNodeDoc(typ) == nil {
			t.Errorf("trigger %q has no `ref node` entry", typ)
		}
	}
}

// Deprecated ai.* stubs must say so in the manual instead of documenting a
// provider config that no longer runs.
func TestRefDocsFlagDeprecatedAINodes(t *testing.T) {
	t.Parallel()
	for _, typ := range []string{"ai.chat", "ai.extract", "ai.classify", "ai.transform", "ai.agent", "ai.embed"} {
		d := FindNodeDoc(typ)
		if d == nil {
			t.Errorf("%s: missing ref entry", typ)
			continue
		}
		if !strings.Contains(d.Short, "DEPRECATED") {
			t.Errorf("%s: ref Short = %q, want it to start with DEPRECATED", typ, d.Short)
		}
	}
}
