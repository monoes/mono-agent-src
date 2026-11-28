package surfaces

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

func act(name, surface string, ctx map[string]any) a2ui.Action {
	return a2ui.Action{Name: name, SurfaceID: surface, SourceComponentID: "b", Context: ctx}
}

func TestRouteAcceptsGenuineActions(t *testing.T) {
	cases := []struct {
		a    a2ui.Action
		ref  chatevents.UIRef
		want string
	}{
		{act("hil.approve", "hil-482", map[string]any{"id": "482", "message": "edited"}), chatevents.UIRef{Kind: RefHIL, ID: "482"}, "hil.approve"},
		{act("hil.reject", "send-7c1e", map[string]any{"id": "7c1e"}), chatevents.UIRef{Kind: RefSend, ID: "7c1e"}, "hil.reject"},
		{act("mission.stop", "mission-17", map[string]any{"sessionId": "17"}), chatevents.UIRef{Kind: RefMissionPlan, ID: "17"}, "mission.stop"},
		{act("mission.close", "mission-report-17", map[string]any{"sessionId": "17"}), chatevents.UIRef{Kind: RefMissionReport, ID: "17"}, "mission.close"},
		{act("system.answer", "ask-q9", map[string]any{"questionId": "q9", "q0": []any{"eu"}, "q1": "note"}), chatevents.UIRef{Kind: RefAsk, ID: "q9"}, "system.answer"},
	}
	for _, c := range cases {
		cmd, err := Route(c.a, c.ref)
		if err != nil || cmd.Kind != c.want || cmd.ID != c.ref.ID {
			t.Errorf("%s: %+v %v", c.want, cmd, err)
		}
	}
	cmd, _ := Route(cases[4].a, cases[4].ref)
	if cmd.Fields["q0"] != "eu" || cmd.Fields["q1"] != "note" || len(cmd.Fields) != 2 {
		t.Fatalf("fields %v", cmd.Fields)
	}
	cmd, _ = Route(cases[0].a, cases[0].ref)
	if cmd.Fields["message"] != "edited" || len(cmd.Fields) != 1 {
		t.Fatalf("fields %v", cmd.Fields)
	}
}

// Each refusal is checked for its own reason, so removing that guard makes
// exactly its case fail.
func TestRouteRefusals(t *testing.T) {
	hil := chatevents.UIRef{Kind: RefHIL, ID: "482"}
	cases := map[string]struct {
		a    a2ui.Action
		ref  chatevents.UIRef
		want string
	}{
		"forged id":         {act("hil.approve", "hil-482", map[string]any{"id": "999"}), hil, "forged"},
		"missing id":        {act("hil.approve", "hil-482", map[string]any{"message": "x"}), hil, "forged or missing"},
		"id not a string":   {act("hil.approve", "hil-482", map[string]any{"id": 482}), hil, "forged or missing"},
		"other surface":     {act("hil.approve", "hil-999", map[string]any{"id": "482"}), hil, "came from surface"},
		"unknown action":    {act("hil.delete", "hil-482", map[string]any{"id": "482"}), hil, "not a host action"},
		"wrong record kind": {act("mission.stop", "hil-482", map[string]any{"sessionId": "482"}), hil, "does not apply"},
		"reject with field": {act("hil.reject", "hil-482", map[string]any{"id": "482", "x": "y"}), hil, "takes no field"},
		"object value":      {act("hil.approve", "hil-482", map[string]any{"id": "482", "m": map[string]any{"a": "b"}}), hil, "must be a string"},
		"number value":      {act("hil.approve", "hil-482", map[string]any{"id": "482", "m": 5}), hil, "must be a string"},
		"long value":        {act("hil.approve", "hil-482", map[string]any{"id": "482", "m": strings.Repeat("a", a2ui.DefaultLimits.MaxReturnedString+1)}), hil, "byte cap"},
		"answer key":        {act("system.answer", "ask-q9", map[string]any{"questionId": "q9", "admin": "x"}), chatevents.UIRef{Kind: RefAsk, ID: "q9"}, "takes no field"},
		"multi choice":      {act("system.answer", "ask-q9", map[string]any{"questionId": "q9", "q0": []any{"a", "b"}}), chatevents.UIRef{Kind: RefAsk, ID: "q9"}, "must be a string"},
		"list on approve":   {act("hil.approve", "hil-482", map[string]any{"id": "482", "m": []any{"a"}}), hil, "must be a string"},
		"empty ref":         {act("hil.approve", "hil-", map[string]any{"id": ""}), chatevents.UIRef{Kind: RefHIL}, "no id"},
	}
	for name, c := range cases {
		_, err := Route(c.a, c.ref)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", name, c.want, err)
		}
	}
}

// A real genuine action produced through the whole chain: build, register,
// parse the renderer's JSON, check against the registry, route.
func TestRouteEndToEnd(t *testing.T) {
	msgs, _ := HIL(sampleHIL())
	reg := applyAll(t, a2ui.FromSystem, msgs)
	raw := []byte(`{"version":"1.0","action":{"name":"hil.approve","surfaceId":"hil-482","sourceComponentId":"btn-approve","timestamp":"t","context":{"id":"482","message":"Hi!","delay_minutes":"7"}}}`)
	a, err := a2ui.ParseAction(raw, a2ui.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.CheckAction(a); err != nil {
		t.Fatal(err)
	}
	cmd, err := Route(a, chatevents.UIRef{Kind: RefHIL, ID: "482"})
	if err != nil || cmd.Fields["delay_minutes"] != "7" {
		t.Fatalf("%+v %v", cmd, err)
	}
	forged := strings.Replace(string(raw), `"id":"482"`, `"id":"1"`, 1)
	a, _ = a2ui.ParseAction([]byte(forged), a2ui.DefaultLimits)
	if _, err := reg.CheckAction(a); err != nil {
		t.Fatal("the registry guard is not what stops a forged id; Route is: ", err)
	}
	if _, err := Route(a, chatevents.UIRef{Kind: RefHIL, ID: "482"}); err == nil {
		t.Fatal("forged id routed")
	}
}

// Edited text of 20 KiB goes builder -> registry -> ParseAction -> Route whole.
func TestLargeEditedTextRoundTrips(t *testing.T) {
	it := sampleHIL()
	it.EditableData = map[string]any{"message": strings.Repeat("x", 100)}
	msgs, err := HIL(it)
	if err != nil {
		t.Fatal(err)
	}
	reg := applyAll(t, a2ui.FromSystem, msgs)
	big := strings.Repeat("é", 10<<10) // 20 KiB
	raw, _ := marshal(map[string]any{"version": "1.0", "action": map[string]any{"name": "hil.approve", "surfaceId": "hil-482",
		"sourceComponentId": btnApprove, "context": map[string]any{"id": "482", "message": big}}})
	a, err := a2ui.ParseAction(raw, a2ui.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.CheckAction(a); err != nil {
		t.Fatal(err)
	}
	cmd, err := Route(a, chatevents.UIRef{Kind: RefHIL, ID: "482"})
	if err != nil || cmd.Fields["message"] != big {
		t.Fatalf("round trip lost text: %v", err)
	}
}
