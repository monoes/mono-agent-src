package main

// What the page receives from the API bindings: Wails marshals a binding's result
// with encoding/json, so what a CLI that predates a field does not send stays
// absent, what a current CLI says is kept (zeros and false included), and each
// model's capabilities arrive as a list.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAPIDocs installs a monoagentcli stand-in that answers `api status` and
// `api models` with the given documents.
func fakeAPIDocs(t *testing.T, status, models string) {
	t.Helper()
	script := "#!/bin/sh\ncase \"$*\" in\n" +
		"  *\" api status\"*) echo '" + status + "';;\n" +
		"  *\" api models\"*) echo '" + models + "';;\n" +
		"  *) echo 'unexpected' >&2; exit 2;;\nesac\n"
	bin := filepath.Join(t.TempDir(), fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
}

// wire is what the page receives: Wails marshals a binding's result with
// encoding/json, so a field the CLI did not send must not come out at all.
func wire(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func absent(t *testing.T, what string, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, has := m[k]; has {
			t.Errorf("%s: %q must be absent when the CLI did not send it, got %v", what, k, v)
		}
	}
}

// What a CLI that predates a field does not send stays absent in what the page
// receives, and is not turned into false, 0 or an empty object: the page tells
// "the CLI said no" from "the CLI said nothing".
func TestAPIAbsentStaysAbsent(t *testing.T) {
	fakeAPIDocs(t,
		`{"v":1,"profile":"work","keys":{"active":0},"daemon":{"running":false},"listeners":[`+
			`{"name":"main","addr":"127.0.0.1:9322","loopback":true,"v1":true,"confinement":"any","context_confinement":"chat-only","confinement_source":"environment","reachable":false,"v1_answers":false}]}`,
		`{"v":1,"policy":{"for":"loopback","confinement":"any","context_confinement":"chat-only","source":"shell"},"models":[`+
			`{"id":"claude/default","runtime":"claude","model":"default","label":"Default","confinement":"chat-only","validated":false,"allowed":true,"context_allowed":true}],`+
			`"auto":{"available":true,"key_source":"vault"}}`)
	a := newTestApp(t)
	a.ctx = context.Background()

	st, err := a.APIStatus()
	if err != nil {
		t.Fatal(err)
	}
	s := wire(t, st)
	absent(t, "status", s, "auto")
	absent(t, "listener", s["listeners"].([]any)[0].(map[string]any), "scheme", "auto_confinement")

	m, err := a.APIModels("", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	mm := wire(t, m)
	absent(t, "policy", mm["policy"].(map[string]any), "auto_confinement")
	absent(t, "model", mm["models"].([]any)[0].(map[string]any), "auto_allowed", "capabilities")
	absent(t, "auto", mm["auto"].(map[string]any), "candidates", "held_back", "confinement", "missing")
	if mm["auto"].(map[string]any)["available"] != true {
		t.Errorf("auto = %v", mm["auto"])
	}
}

// What each model can do (`capabilities` of the models report) reaches the page as
// a list, in the order the CLI gave it and with whatever it names, so that a
// capability a later CLI adds is not lost. A model without the field, as a CLI that
// predates it lists it, has none in what the page receives: not null, not empty.
func TestAPIModelsCarryCapabilities(t *testing.T) {
	model := func(id, caps string) string {
		return `{"id":"` + id + `","runtime":"x","model":"m","label":"l","confinement":"chat-only","validated":false,"allowed":true,"context_allowed":true,"auto_allowed":true` + caps + `}`
	}
	ids := []string{"text", "image", "tools", "both", "future"}
	want := []string{"text", "text,image", "text,tools", "text,image,tools", "text,audio"}
	var models []string
	for i, id := range ids {
		models = append(models, model(id, `,"capabilities":["`+strings.ReplaceAll(want[i], ",", `","`)+`"]`))
	}
	models = append(models, model("older", ""))
	fakeAPIDocs(t,
		`{"v":1,"profile":"work","keys":{"active":0},"daemon":{"running":false},"listeners":[]}`,
		`{"v":1,"policy":{"for":"loopback","confinement":"any","context_confinement":"chat-only","auto_confinement":"chat-only","source":"shell"},"models":[`+
			strings.Join(models, ",")+`],"auto":{"available":false}}`)
	a := newTestApp(t)
	a.ctx = context.Background()

	m, err := a.APIModels("", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Models) != len(ids)+1 {
		t.Fatalf("models = %d", len(m.Models))
	}
	page := wire(t, m)["models"].([]any)
	for i, id := range ids {
		list, ok := page[i].(map[string]any)["capabilities"].([]any)
		if !ok {
			t.Fatalf("%s: capabilities = %v", id, page[i].(map[string]any)["capabilities"])
		}
		var got []string
		for _, c := range list {
			got = append(got, c.(string))
		}
		if strings.Join(got, ",") != want[i] {
			t.Errorf("%s: capabilities = %v, want %s", id, got, want[i])
		}
	}
	absent(t, "a model of a CLI that predates capabilities", page[len(ids)].(map[string]any), "capabilities")
}

// What a current CLI says is kept, zeros and false included: they are values.
func TestAPIPresentZerosAreKept(t *testing.T) {
	fakeAPIDocs(t,
		`{"v":1,"profile":"work","keys":{"active":0},"daemon":{"running":true},"auto":{"available":false,"missing":"a Jev key"},"listeners":[`+
			`{"name":"v1","addr":"0.0.0.0:9443","loopback":false,"v1":true,"confinement":"chat-only","context_confinement":"chat-only","auto_confinement":"chat-only","confinement_source":"daemon","scheme":"https","reachable":true,"v1_answers":true}]}`,
		`{"v":1,"policy":{"for":"network","confinement":"chat-only","context_confinement":"chat-only","auto_confinement":"chat-only","source":"shell"},"models":[`+
			`{"id":"codex/default","runtime":"codex","model":"default","label":"x","confinement":"sandboxed","validated":false,"allowed":false,"context_allowed":false,"auto_allowed":false}],`+
			`"auto":{"available":true,"key_source":"env","confinement":"chat-only","candidates":0,"held_back":0}}`)
	a := newTestApp(t)
	a.ctx = context.Background()

	st, err := a.APIStatus()
	if err != nil {
		t.Fatal(err)
	}
	s := wire(t, st)
	if auto, ok := s["auto"].(map[string]any); !ok || auto["available"] != false || auto["missing"] != "a Jev key" {
		t.Errorf("status auto = %v", s["auto"])
	}
	if l := s["listeners"].([]any)[0].(map[string]any); l["scheme"] != "https" || l["auto_confinement"] != "chat-only" {
		t.Errorf("listener = %v", l)
	}

	m, err := a.APIModels("network", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	mm := wire(t, m)
	if v, has := mm["models"].([]any)[0].(map[string]any)["auto_allowed"]; !has || v != false {
		t.Errorf("auto_allowed false must reach the page as false, got %v (present %v)", v, has)
	}
	auto := mm["auto"].(map[string]any)
	for _, k := range []string{"candidates", "held_back"} {
		if v, has := auto[k]; !has || v != float64(0) {
			t.Errorf("auto.%s 0 must reach the page as 0, got %v (present %v)", k, v, has)
		}
	}
}
