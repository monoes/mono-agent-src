package coderhost

import (
	"reflect"
	"strings"
	"testing"
)

func TestDefaultSettingsAreValid(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Fatal(err)
	}
	d := DefaultSettings()
	if d.CheckinMinutes != 15 || !d.SkipIfUnchanged || d.AutonomyPreset != "ask-on-risk" || d.MaxDepth != 2 {
		t.Fatalf("defaults = %+v", d)
	}
}

func TestApplyRefusesWhatIsNotASetting(t *testing.T) {
	d := DefaultSettings()
	bad := map[string]string{
		"unknown key":          `{"checkin_minute": 5}`,
		"wrong type":           `{"checkin_minutes": "5"}`,
		"not an object":        `[1]`,
		"minutes too large":    `{"checkin_minutes": 99999}`,
		"negative minutes":     `{"checkin_minutes": -1}`,
		"silent too small":     `{"silent_minutes": 1}`,
		"bad hours":            `{"active_hours": "9-5"}`,
		"bad hour value":       `{"active_hours": "25:00-26:00"}`,
		"bad preset":           `{"autonomy_preset": "yolo"}`,
		"bad class":            `{"autonomy_classes": {"whatever": {"who": "user", "fallback": "park"}}}`,
		"class without who":    `{"autonomy_classes": {"ambiguity": {"fallback": "park"}}}`,
		"bad trigger":          `{"checkin_triggers": ["child_died"]}`,
		"duplicate trigger":    `{"checkin_triggers": ["budget_80", "budget_80"]}`,
		"depth over the limit": `{"max_depth": 3}`,
		"no concurrency":       `{"max_concurrent": 0}`,
		"alias with a slash":   `{"mesh_alias": "a/b"}`,
		"alias with a space":   `{"mesh_alias": "a b"}`,
		"bad accept":           `{"mesh_accept": "everyone"}`,
	}
	for name, patch := range bad {
		if _, _, err := d.Apply([]byte(patch)); err == nil {
			t.Errorf("%s: %s was accepted", name, patch)
		}
	}
}

func TestApplyChangesOnlyWhatThePatchNamesAndReportsIt(t *testing.T) {
	d := DefaultSettings()
	got, changed, err := d.Apply([]byte(`{"checkin_minutes": 0, "mesh_alias": "boss-1", "autonomy_classes": {"risky_action": {"who": "user", "fallback": "park"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckinMinutes != 0 || got.MeshAlias != "boss-1" || got.AutonomyClasses["risky_action"].Who != "user" {
		t.Fatalf("applied = %+v", got)
	}
	got.CheckinMinutes, got.MeshAlias, got.AutonomyClasses = d.CheckinMinutes, d.MeshAlias, d.AutonomyClasses
	if !reflect.DeepEqual(got, d) {
		t.Fatalf("the patch changed more than it named:\n got %+v\nwant %+v", got, d)
	}
	if !reflect.DeepEqual(changed, []string{"autonomy_classes", "checkin_minutes", "mesh_alias"}) {
		t.Fatalf("changed = %v", changed)
	}
	if ops := TouchesOperatorOnly(changed); !reflect.DeepEqual(ops, []string{"autonomy_classes"}) {
		t.Fatalf("operator-only = %v", ops)
	}
}

func TestApplyShareNoStorageWithTheOriginalAndReplacesLists(t *testing.T) {
	d := DefaultSettings()
	d.AutonomyClasses["ambiguity"] = AutonomyChoice{Who: "model", Fallback: "take_default"}
	a, _, err := d.Apply([]byte(`{"autonomy_classes": {"risky_action": {"who": "user", "fallback": "park"}}, "checkin_triggers": ["budget_80"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := a.AutonomyClasses["ambiguity"]; kept || len(a.AutonomyClasses) != 1 {
		t.Fatalf("a patched map must replace the old one: %+v", a.AutonomyClasses)
	}
	if !reflect.DeepEqual(a.CheckinTriggers, []string{"budget_80"}) {
		t.Fatalf("triggers = %v", a.CheckinTriggers)
	}
	if len(d.AutonomyClasses) != 1 || d.AutonomyClasses["ambiguity"].Who != "model" || len(d.CheckinTriggers) != len(CheckinEvents) {
		t.Fatalf("the original was changed: %+v / %v", d.AutonomyClasses, d.CheckinTriggers)
	}
	b, _, _ := d.Apply([]byte(`{"mesh_alias": "x"}`)) // an unpatched map is kept, as a copy
	b.AutonomyClasses["child_conflict"] = AutonomyChoice{Who: "user", Fallback: "park"}
	if _, leaked := d.AutonomyClasses["child_conflict"]; leaked {
		t.Fatal("an applied copy shares its map with the original")
	}
}

func TestSettingsAreStoredPerSessionAndReadingCreatesNothing(t *testing.T) {
	_, _, st := newHost(t)
	got, err := st.GetSettings("p", "c1")
	if err != nil || !reflect.DeepEqual(got, DefaultSettings()) {
		t.Fatalf("no session: %+v, %v", got, err)
	}
	if _, ok, _ := st.GetSession("p", "c1"); ok {
		t.Fatal("reading settings created a session")
	}
	next, _, err := got.Apply([]byte(`{"checkin_minutes": 5, "mesh_visible": false}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSettings("p", "c1", next); err != nil {
		t.Fatal(err)
	}
	back, err := st.GetSettings("p", "c1")
	if err != nil || back.CheckinMinutes != 5 || back.MeshVisible {
		t.Fatalf("stored = %+v, %v", back, err)
	}
	other, _ := st.GetSettings("p", "c2")
	if other.CheckinMinutes != 15 {
		t.Fatal("settings leaked to another session")
	}
	bad := next
	bad.MaxDepth = 9
	if err := st.SaveSettings("p", "c1", bad); err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Fatalf("an invalid document was stored: %v", err)
	}
	if err := st.SetUnattended("p", "c1", false); err != nil {
		t.Fatal(err)
	}
	if x, _, _ := st.GetSession("p", "c1"); x.Unattended {
		t.Fatal("unattended not cleared")
	}
}
