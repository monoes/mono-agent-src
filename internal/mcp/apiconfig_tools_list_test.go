package mcp

// How the tools of the API's settings are listed: the read-only ones are there without
// --allow-mutations and say so, the others are omitted from tools/list and refused by name without
// it, and every one carries the annotations that let a host gate it.

import (
	"reflect"
	"strings"
	"testing"
)

// readOnlyAPIConfigTools are the tools that change nothing; mutatingAPIConfigTools the ones that do,
// with the annotations each must carry.
var readOnlyAPIConfigTools = []string{"api_status", "api_config_get"}

var mutatingAPIConfigTools = map[string]map[string]bool{
	"api_config_set":   {"readOnlyHint": false},
	"api_config_apply": {"readOnlyHint": false, "destructiveHint": true},
	"api_auto_set":     {"readOnlyHint": false},
}

func TestAPIConfigReadToolsAreListedWithoutTheFlagAndAnnotated(t *testing.T) {
	f := newConfigFixture(t, configSetup{readOnly: true})
	names := toolsListNames(t, f.Server)
	want := map[string]bool{"readOnlyHint": true, "idempotentHint": true}
	for _, name := range readOnlyAPIConfigTools {
		if !names[name] {
			t.Errorf("%s changes nothing and must be listed without --allow-mutations", name)
		}
		var got map[string]bool
		for _, def := range toolDefinitions(false) {
			if def["name"] == name {
				got, _ = def["annotations"].(map[string]bool)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s annotations %v, want %v", name, got, want)
		}
	}
}

// The tools that change something exist only with --allow-mutations, like the other mutating tools:
// omitted from tools/list, refused by name without it and doing nothing, and annotated so that a
// host can gate them.
func TestAPIConfigMutatingToolsAreGatedAndAnnotated(t *testing.T) {
	closed := newConfigFixture(t, configSetup{readOnly: true})
	open := newConfigFixture(t, configSetup{})
	closedNames, openNames := toolsListNames(t, closed.Server), toolsListNames(t, open.Server)
	for name, want := range mutatingAPIConfigTools {
		if closedNames[name] {
			t.Errorf("%s changes something and must not be listed without --allow-mutations", name)
		}
		if !openNames[name] {
			t.Errorf("%s must be listed with --allow-mutations", name)
		}
		var got map[string]bool
		for _, def := range toolDefinitions(true) {
			if def["name"] == name {
				got, _ = def["annotations"].(map[string]bool)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s annotations %v, want %v", name, got, want)
		}
		for _, tl := range apiConfigTools() {
			if tl.name == name && !tl.mutating {
				t.Errorf("%s is not marked mutating", name)
			}
		}
	}
}

// What decides whether a key created with context may be served tool calling, or models that are
// not chat-only, is a setting now, and a model a user asks about a key is told which, and where to see it.
func TestAPIKeyCreateDescriptionPointsAtTheSettingsThatDecideWhatAContextKeyMayUse(t *testing.T) {
	var d string
	for _, tl := range apiTools() {
		if tl.name == "api_key_create" {
			d = apiToolText(tl)
		}
	}
	for _, want := range []string{"context_confinement", "confinement", "api_config_get", "api_config_set", "tool calling", "--context-confinement"} {
		if !strings.Contains(d, want) {
			t.Errorf("the description of api_key_create does not mention %q: %s", want, d)
		}
	}
}

// The list of models reads the saved settings too, so it stops at a row that cannot be read.
func TestAPIModelsListDescriptionSaysWhatADamagedRowMeans(t *testing.T) {
	var d string
	for _, tl := range apiTools() {
		if tl.name == "api_models_list" {
			d = apiToolText(tl)
		}
	}
	for _, want := range []string{"the saved settings are damaged", "monoagentcli api config unset --all --yes", "--allow-api-exposure", "newer version"} {
		if !strings.Contains(d, want) {
			t.Errorf("the description of api_models_list does not mention %q: %s", want, d)
		}
	}
}

// A description is all a model has of a tool: each of these says what the tool does and what it
// does not.
func TestAPIConfigToolDescriptionsSayWhatTheyDoNot(t *testing.T) {
	descriptions := map[string]string{}
	for _, tl := range apiConfigTools() {
		descriptions[tl.name] = apiToolText(tl)
	}
	// What a model tells the user when the saved settings cannot be read, in every tool that reads them.
	damaged := []string{"the saved settings are damaged", "monoagentcli api config unset --all --yes", "--allow-api-exposure", "newer version"}
	for name, wants := range map[string][]string{
		"api_status":     append([]string{"api status --json", "changes nothing", "key"}, damaged...),
		"api_config_get": append([]string{"api config show --json", "changes nothing", "pending_restart", "api_config_apply restarts it", "overridden", "api_config_set"}, damaged...),
		"api_config_set": append([]string{
			"api config set|unset --json", "--allow-api-exposure", "No argument can allow it", "restarts nothing", "api_config_apply",
			"desktop app", "monoagentcli api config set ... --yes", "sk-ma-", "reach further", "Two calls at once both land",
			// What reaches further: a move to another host or to every interface, and removing what cannot be read.
			"another host", "every interface", "removing saved settings that cannot be read", "removed_unreadable_row",
		}, damaged...),
	} {
		d, ok := descriptions[name]
		if !ok {
			t.Errorf("%s is not one of the API settings tools", name)
			continue
		}
		for _, want := range wants {
			if !strings.Contains(d, want) {
				t.Errorf("the description of %s does not mention %q: %s", name, want, d)
			}
		}
	}
}
