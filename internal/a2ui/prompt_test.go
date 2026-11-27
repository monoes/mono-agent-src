package a2ui

import (
	"strings"
	"testing"
)

func TestPromptSnippetListsEveryCatalogComponentAndEnum(t *testing.T) {
	s := PromptSnippet()
	for name, spec := range catalog {
		if !strings.Contains(s, "- "+name+": ") {
			t.Errorf("component %s missing from the prompt", name)
		}
		for prop, ps := range spec.props {
			if !strings.Contains(s, prop) {
				t.Errorf("%s.%s missing from the prompt", name, prop)
			}
			for _, e := range ps.enum {
				if !strings.Contains(s, e) {
					t.Errorf("%s.%s enum value %q missing", name, prop, e)
				}
			}
		}
	}
}

func TestPromptSnippetStatesTheRules(t *testing.T) {
	s := PromptSnippet()
	for _, want := range []string{`"root"`, `"@path"`, "hil.", "mission.", "system.", "markdown", "next message", "surfaceId"} {
		if !strings.Contains(s, want) {
			t.Errorf("prompt does not mention %q", want)
		}
	}
	// ~1800 tokens at 4 chars each; the snippet is sent on every turn.
	if len(s) > 7000 {
		t.Errorf("prompt is %d bytes, over the ~1800 token budget", len(s))
	}
}

// The example the field list of the prompt describes is accepted as it is written.
func TestPromptSnippetPayloadFieldsAreTheValidatorsOwn(t *testing.T) {
	s := PromptSnippet()
	for _, want := range []string{"createSurface {surfaceId, components?, dataModel?}", "updateComponents {surfaceId, components}", "updateDataModel {surfaceId, path, value}", "deleteSurface {surfaceId}"} {
		if !strings.Contains(s, want) {
			t.Errorf("prompt does not state %q", want)
		}
	}
	reg(t, FromAgent,
		`{"version":"1.0","createSurface":{"surfaceId":"s1","components":[{"id":"root","component":"Divider"}],"dataModel":{}}}`,
		`{"version":"1.0","updateComponents":{"surfaceId":"s1","components":[{"id":"root","component":"Divider"}]}}`,
		`{"version":"1.0","updateDataModel":{"surfaceId":"s1","path":"/a","value":1}}`,
		`{"version":"1.0","deleteSurface":{"surfaceId":"s1"}}`)
}
