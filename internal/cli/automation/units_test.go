package automation

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/action"
	"testing"
	"testing/fstest"
)

func TestAutomationParseInputsBothForms(t *testing.T) {
	t.Parallel()
	raws := []json.RawMessage{
		json.RawMessage(`"query"`),
		json.RawMessage(`{"name":"limit","type":"number","description":"max","default":10,"ui":{"label":"Limit"}}`),
		json.RawMessage(`{"type":"string"}`), // no name: dropped
	}
	got := parseInputs(raws, true)
	if len(got) != 2 {
		t.Fatalf("got %d inputs", len(got))
	}
	if got[0].Name != "query" || got[0].Type != "string" || !got[0].Required || got[0].UI == nil {
		t.Errorf("string form: %+v", got[0])
	}
	if got[1].Type != "number" || got[1].Default != float64(10) || got[1].UI["label"] != "Limit" {
		t.Errorf("object form: %+v", got[1])
	}
}

func TestAutomationSessionStatus(t *testing.T) {
	t.Parallel()
	var idx sessionIndex = sessionIndex{}
	if got := idx.lookup("nobody"); got.Status != "logged_out" || got.LoggedIn {
		t.Fatalf("no session = %+v", got)
	}
}

func TestAutomationFixtureCompareAndInputs(t *testing.T) {
	t.Parallel()
	items := []map[string]interface{}{{"id": "1", "n": 2}}
	for _, tc := range []struct {
		want string
		ok   bool
	}{
		{`[{"id":"1","n":2}]`, true},
		{`{"id":"1","n":2}`, true},
		{`{"items":[{"id":"1","n":2}]}`, true},
		{`[{"id":"1","n":3}]`, false},
		{`[]`, false},
	} {
		var w interface{}
		json.Unmarshal([]byte(tc.want), &w)
		if got := outputMatches(items, w); got != tc.ok {
			t.Errorf("outputMatches(%s) = %v", tc.want, got)
		}
	}

	def := &action.ActionDef{Inputs: &action.InputDef{Required: []json.RawMessage{
		json.RawMessage(`"itemID"`), json.RawMessage(`"query"`)}}}
	var want interface{}
	json.Unmarshal([]byte(`[{"itemID":"7","title":"x"}]`), &want)
	fsys := fstest.MapFS{"tests/a.inputs.json": {Data: []byte(`{"query":"q"}`)}}
	in, missing := fixtureInputs(fsys, "a", def, want)
	if len(missing) != 0 || in["itemID"] != "7" || in["query"] != "q" {
		t.Fatalf("inputs %v missing %v", in, missing)
	}
	_, missing = fixtureInputs(fstest.MapFS{}, "a", def, nil)
	if len(missing) != 2 {
		t.Fatalf("missing = %v", missing)
	}
}
