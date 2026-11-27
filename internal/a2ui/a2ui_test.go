package a2ui

import (
	"fmt"
	"strings"
	"testing"
)

const create = `{"version":"1.0","createSurface":{"surfaceId":"s1"}}`

func reg(t *testing.T, prov Provenance, msgs ...string) *Registry {
	t.Helper()
	r := NewRegistry(DefaultLimits)
	for _, m := range msgs {
		if _, err := r.Handle([]byte(m), prov); err != nil {
			t.Fatalf("setup %s: %v", m, err)
		}
	}
	return r
}

func comps(s string) string {
	return `{"version":"1.0","updateComponents":{"surfaceId":"s1","components":[` + s + `]}}`
}

const approve = `{"id":"root","component":"Column","children":["t","f","go"]},
 {"id":"t","component":"Text","text":"Send DM?"},
 {"id":"f","component":"TextField","label":"Text","value":{"@path":"/draft"}},
 {"id":"gl","component":"Text","text":"Approve"},
 {"id":"go","component":"Button","child":"gl","variant":"primary","action":{"event":{"name":"%s","context":{"id":"482","text":{"@path":"/draft"}}}}}`

func TestValidSurfaceRoundTrip(t *testing.T) {
	r := reg(t, FromSystem, create, comps(fmt.Sprintf(approve, "hil.approve")),
		`{"version":"1.0","updateDataModel":{"surfaceId":"s1","path":"/draft","value":"Hi Sara"}}`)
	s := r.Surface("s1")
	if err := s.Check(DefaultLimits); err != nil {
		t.Fatal(err)
	}
	if s.Data.(map[string]any)["draft"] != "Hi Sara" {
		t.Fatalf("data model %v", s.Data)
	}
}

func TestCreateWithComponentsAndNumbers(t *testing.T) {
	m := `{"version":"1.0","createSurface":{"surfaceId":"p","catalogId":"` + CatalogID + `","components":[
	 {"id":"root","component":"Progress","value":{"@path":"/n"},"max":10}],"dataModel":{"n":3}}}`
	r := reg(t, FromSystem, m)
	if err := r.Surface("p").Check(DefaultLimits); err != nil {
		t.Fatal(err)
	}
}

func TestRejectedMessages(t *testing.T) {
	r := reg(t, FromAgent, create)
	cases := map[string]string{
		"wrong version":      `{"version":"0.9","createSurface":{"surfaceId":"x"}}`,
		"two payloads":       `{"version":"1.0","createSurface":{"surfaceId":"x"},"deleteSurface":{"surfaceId":"x"}}`,
		"other catalog":      `{"version":"1.0","createSurface":{"surfaceId":"x","catalogId":"https://evil.example/c.json"}}`,
		"unknown field":      `{"version":"1.0","createSurface":{"surfaceId":"x","theme":{}}}`,
		"bad surface id":     `{"version":"1.0","createSurface":{"surfaceId":"a b"}}`,
		"trailing data":      create + `{}`,
		"unknown component":  comps(`{"id":"root","component":"Iframe","src":"x"}`),
		"unknown property":   comps(`{"id":"root","component":"Text","text":"a","style":"x"}`),
		"missing required":   comps(`{"id":"root","component":"Text"}`),
		"bad enum":           comps(`{"id":"root","component":"Text","text":"a","variant":"h1"}`),
		"function call":      comps(`{"id":"root","component":"Text","text":{"@call":"formatDate","args":{}}}`),
		"binding + extra":    comps(`{"id":"root","component":"Text","text":{"@path":"/a","x":1}}`),
		"bad pointer":        comps(`{"id":"root","component":"Text","text":{"@path":"a/b"}}`),
		"template children":  comps(`{"id":"root","component":"Column","children":{"componentId":"x","path":"/l"}}`),
		"function action":    comps(`{"id":"root","component":"Button","child":"x","action":{"functionCall":{"@call":"openUrl"}}}`),
		"reserved hil":       comps(fmt.Sprintf(approve, "hil.approve")),
		"reserved mission":   comps(`{"id":"b","component":"Button","child":"x","action":{"event":{"name":"mission.approve"}}}`),
		"unknown surface":    `{"version":"1.0","updateDataModel":{"surfaceId":"nope","value":1}}`,
		"duplicate surface":  create,
		"update without val": `{"version":"1.0","updateDataModel":{"surfaceId":"s1","path":"/a"}}`,
	}
	for name, raw := range cases {
		if _, err := r.Handle([]byte(raw), FromAgent); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSystemMayUseReservedActionNames(t *testing.T) {
	reg(t, FromSystem, create, comps(fmt.Sprintf(approve, "hil.approve")))
}

func TestAgentActionNameAllowed(t *testing.T) {
	reg(t, FromAgent, create, comps(fmt.Sprintf(approve, "submit_form")))
}

func TestProvenanceCannotChange(t *testing.T) {
	r := reg(t, FromSystem, create)
	if _, err := r.Handle([]byte(comps(`{"id":"root","component":"Text","text":"hi"}`)), FromAgent); err == nil {
		t.Fatal("agent updated a system surface")
	}
}

// The next tests prove each cap by what it bounds: the rejection must happen
// for the cap's own reason, so reverting the cap fails the test.

func TestMessageCapRunsBeforeDecoding(t *testing.T) {
	huge := []byte(strings.Repeat("x", DefaultLimits.MaxMessageBytes+1)) // not JSON at all
	_, err := ParseMessage(huge, DefaultLimits)
	if err == nil || !strings.Contains(err.Error(), "byte cap") {
		t.Fatalf("want the size cap to fire before the JSON decoder, got %v", err)
	}
}

func TestComponentCap(t *testing.T) {
	r := reg(t, FromAgent, create)
	var b strings.Builder
	for i := 0; i <= DefaultLimits.MaxComponents; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"c%d","component":"Text","text":"x"}`, i)
	}
	if _, err := r.Handle([]byte(comps(b.String())), FromAgent); err == nil || !strings.Contains(err.Error(), "components") {
		t.Fatalf("got %v", err)
	}
	if got := len(r.Surface("s1").Components); got != 0 {
		t.Fatalf("a rejected update left %d components behind", got)
	}
}

func TestStringAndDataModelCaps(t *testing.T) {
	r := reg(t, FromAgent, create)
	long := strings.Repeat("a", DefaultLimits.MaxStringBytes+1)
	if _, err := r.Handle([]byte(comps(`{"id":"root","component":"Text","text":"`+long+`"}`)), FromAgent); err == nil {
		t.Fatal("long literal accepted")
	}
	big := strings.Repeat("a", 3000)
	for i := 0; i < 20; i++ {
		raw := fmt.Sprintf(`{"version":"1.0","updateDataModel":{"surfaceId":"s1","path":"/k%d","value":"%s"}}`, i, big)
		_, err := r.Handle([]byte(raw), FromAgent)
		if err != nil {
			if !strings.Contains(err.Error(), "data model") {
				t.Fatalf("got %v", err)
			}
			if _, has := r.Surface("s1").Data.(map[string]any)[fmt.Sprintf("k%d", i)]; has {
				t.Fatal("the rejected write stayed in the model")
			}
			return
		}
	}
	t.Fatal("data model grew past its cap")
}

func TestSurfaceCap(t *testing.T) {
	r := NewRegistry(DefaultLimits)
	for i := 0; i <= MaxSurfaces; i++ {
		_, err := r.Handle([]byte(fmt.Sprintf(`{"version":"1.0","createSurface":{"surfaceId":"s%d"}}`, i)), FromAgent)
		if (i < MaxSurfaces) != (err == nil) {
			t.Fatalf("surface %d: %v", i, err)
		}
	}
}

func TestStructureChecks(t *testing.T) {
	cases := map[string]string{
		"no root":  `{"id":"a","component":"Text","text":"x"}`,
		"dangling": `{"id":"root","component":"Column","children":["ghost"]}`,
		"cycle":    `{"id":"root","component":"Column","children":["a"]},{"id":"a","component":"Column","children":["root"]}`,
	}
	for name, c := range cases {
		s := reg(t, FromAgent, create, comps(c)).Surface("s1")
		err := s.Check(DefaultLimits)
		if err == nil {
			t.Errorf("%s: passed", name)
		} else if name == "cycle" && !strings.Contains(err.Error(), "contains itself") {
			t.Errorf("cycle was caught by the wrong guard: %v", err)
		}
	}
	var b strings.Builder
	n := DefaultLimits.MaxDepth + 2
	for i := 0; i < n; i++ {
		id := "c" + fmt.Sprint(i)
		if i == 0 {
			id = "root"
		}
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":%q,"component":"Card","child":"c%d"}`, id, i+1)
	}
	fmt.Fprintf(&b, `,{"id":"c%d","component":"Text","text":"leaf"}`, n)
	if err := reg(t, FromAgent, create, comps(b.String())).Surface("s1").Check(DefaultLimits); err == nil {
		t.Error("deep nesting passed")
	}
}

func actionJSON(name, surface, src string) []byte {
	return []byte(fmt.Sprintf(`{"version":"1.0","action":{"name":%q,"surfaceId":%q,"sourceComponentId":%q,
	 "timestamp":"2026-11-24T10:00:00Z","context":{"id":"482"}}}`, name, surface, src))
}

func TestActionGuard(t *testing.T) {
	r := reg(t, FromSystem, create, comps(fmt.Sprintf(approve, "hil.approve")))
	good, err := ParseAction(actionJSON("hil.approve", "s1", "go"), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CheckAction(good); err != nil {
		t.Fatalf("genuine action refused: %v", err)
	}
	for name, a := range map[string]Action{
		"wrong name":      {Name: "hil.reject", SurfaceID: "s1", SourceComponentID: "go"},
		"not a button":    {Name: "hil.approve", SurfaceID: "s1", SourceComponentID: "t"},
		"unknown source":  {Name: "hil.approve", SurfaceID: "s1", SourceComponentID: "zz"},
		"unknown surface": {Name: "hil.approve", SurfaceID: "other", SourceComponentID: "go"},
	} {
		if _, err := r.CheckAction(a); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := r.Handle([]byte(`{"version":"1.0","deleteSurface":{"surfaceId":"s1"}}`), FromSystem); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CheckAction(good); err == nil {
		t.Fatal("a stale action was accepted after the surface closed")
	}
}

func TestParseActionRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"extra envelope key": `{"version":"1.0","action":{"name":"a","surfaceId":"s","sourceComponentId":"b","context":{}},"error":{}}`,
		"callAgentFunction":  `{"version":"1.0","callAgentFunction":{}}`,
		"no context":         `{"version":"1.0","action":{"name":"a","surfaceId":"s","sourceComponentId":"b"}}`,
		"nested context":     `{"version":"1.0","action":{"name":"a","surfaceId":"s","sourceComponentId":"b","context":{"x":{"y":1}}}}`,
		"list of objects":    `{"version":"1.0","action":{"name":"a","surfaceId":"s","sourceComponentId":"b","context":{"x":[{"y":1}]}}}`,
		"unknown field":      `{"version":"1.0","action":{"name":"a","surfaceId":"s","sourceComponentId":"b","context":{},"admin":true}}`,
	} {
		if _, err := ParseAction([]byte(raw), DefaultLimits); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPointerEscapesAndDelete(t *testing.T) {
	r := reg(t, FromAgent, create,
		`{"version":"1.0","updateDataModel":{"surfaceId":"s1","path":"/a~1b/c","value":1}}`,
		`{"version":"1.0","updateDataModel":{"surfaceId":"s1","path":"/a~1b/c","value":null}}`)
	if m := r.Surface("s1").Data.(map[string]any)["a/b"].(map[string]any); len(m) != 0 {
		t.Fatalf("null did not delete: %v", m)
	}
}

func TestReturnedActionCarriesEditedTextAndChoiceLists(t *testing.T) {
	long := strings.Repeat("é", DefaultLimits.MaxReturnedString/2-10) // 2 bytes each, under the cap
	raw := `{"version":"1.0","action":{"name":"hil.approve","surfaceId":"s1","sourceComponentId":"go","context":{"text":"` + long + `","picked":["a","b"]}}}`
	a, err := ParseAction([]byte(raw), DefaultLimits)
	if err != nil {
		t.Fatalf("edited text and a choice list must come back: %v", err)
	}
	if got := a.Context["picked"].([]any); len(got) != 2 {
		t.Fatalf("list lost: %v", got)
	}
	over := strings.Repeat("a", DefaultLimits.MaxReturnedString+1)
	raw = `{"version":"1.0","action":{"name":"a","surfaceId":"s","sourceComponentId":"b","context":{"text":"` + over + `"}}}`
	if _, err := ParseAction([]byte(raw), DefaultLimits); err == nil || !strings.Contains(err.Error(), "byte cap") {
		t.Fatalf("an over-cap returned string was accepted or failed for another reason: %v", err)
	}
	if _, err := ParseAction([]byte(strings.Repeat("x", DefaultLimits.MaxActionBytes+1)), DefaultLimits); err == nil || !strings.Contains(err.Error(), "byte cap") {
		t.Fatalf("action size cap did not fire before decoding: %v", err)
	}
}
