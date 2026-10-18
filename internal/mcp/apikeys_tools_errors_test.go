package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apikeys"
)

// Nothing a caller sends comes back in an error from these tools. A caller may have
// pasted a key into any argument (a model the user showed one has it at
// hand), and a 1 MiB argument must not come back as a multi-megabyte error, as it
// did when the answer quoted the value. Every argument that takes a string gets
// both a key-shaped value and 1 MiB, for every tool of the family, and what comes
// back is short.
func TestNoErrorOfTheAPIToolsRepeatsAnArgument(t *testing.T) {
	pinAPIEnv(t)
	fakeAPIMonomind(t)
	s, dbPath := newAPIKeyServer(t, true)
	existing, _, err := apikeys.NewStore(sideDB(t, dbPath).DB).Create(context.Background(), "default", "app", false)
	if err != nil {
		t.Fatal(err)
	}
	pasted, err := apikeys.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("Q", 1<<20)

	calls := []struct {
		tool string
		args func(v string) map[string]any
	}{
		{"api_key_create", func(v string) map[string]any { return map[string]any{"name": v} }},
		{"api_key_create", func(v string) map[string]any { return map[string]any{"name": "fine", "context": v} }},
		{"api_key_update", func(v string) map[string]any { return map[string]any{"id": v, "name": "other"} }},
		{"api_key_update", func(v string) map[string]any { return map[string]any{"id": existing.ID, "name": v} }},
		{"api_key_update", func(v string) map[string]any { return map[string]any{"id": existing.ID, "context": v} }},
		{"api_key_revoke", func(v string) map[string]any { return map[string]any{"id": v} }},
		{"api_key_list", func(v string) map[string]any { return map[string]any{"include_revoked": v} }},
		{"api_models_list", func(v string) map[string]any { return map[string]any{"for": v} }},
		{"api_models_list", func(v string) map[string]any { return map[string]any{"confinement": v} }},
		{"api_models_list", func(v string) map[string]any { return map[string]any{"context_confinement": v} }},
		{"api_models_list", func(v string) map[string]any { return map[string]any{"auto_confinement": v} }},
	}
	// A key cut to 30 characters is shorter than the longest argument api_models_list
	// looks at, so it reaches the refusals themselves and not the length check.
	short := pasted[:30]
	for _, c := range calls {
		for _, v := range []struct{ what, value string }{{"a key", pasted}, {"a key cut short", short}, {"1 MiB", huge}} {
			text, err := callAPITool(t, s, c.tool, c.args(v.value))
			if err == nil {
				t.Errorf("%s with %s in %v succeeded: %q", c.tool, v.what, sortedFields(c.args("")), scrubbed(text))
				continue
			}
			msg := err.Error()
			if strings.Contains(msg, short) || strings.Contains(msg, strings.Repeat("Q", 16)) {
				t.Errorf("%s with %s in %v: the error repeats the argument", c.tool, v.what, sortedFields(c.args("")))
			}
			if len(msg) > 512 {
				t.Errorf("%s with %s in %v: an error of %d bytes", c.tool, v.what, sortedFields(c.args("")), len(msg))
			}
		}
	}
}
