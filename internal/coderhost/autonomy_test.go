package coderhost

import (
	"strings"
	"testing"
)

// Every preset, every class, every reversibility, with and without a master,
// with no override and with every override the validation lets through: the
// hard boundaries hold in all of them.
func TestDecideBoundariesHoldForEveryPresetClassAndOverride(t *testing.T) {
	overrides := []*AutonomyChoice{nil}
	for _, w := range whoValues {
		for _, f := range fallbackValues {
			overrides = append(overrides, &AutonomyChoice{Who: w, Fallback: f})
		}
	}
	checked := 0
	for _, preset := range AutonomyPresets {
		for _, class := range GapClasses {
			for _, ov := range overrides {
				s := DefaultSettings()
				s.AutonomyPreset = preset
				if ov != nil {
					s.AutonomyClasses = map[string]AutonomyChoice{class: *ov}
					if err := s.Validate(); err != nil {
						// the validation refuses what the boundaries forbid; clamp must still hold for what was stored before
						if !HardClass(class) {
							t.Fatalf("%s/%s/%+v refused: %v", preset, class, *ov, err)
						}
					}
				}
				for _, rev := range []bool{true, false} {
					for _, master := range []bool{true, false} {
						d := Decide(s, class, rev, master)
						checked++
						if !oneOf(d.Answerer, whoValues) || !oneOf(d.Fallback, fallbackValues) {
							t.Fatalf("%s/%s: %+v is not a policy answer", preset, class, d)
						}
						if HardClass(class) {
							if d.Answerer == WhoModel {
								t.Errorf("%s/%s/%+v rev=%v: the model answers a %s gap", preset, class, ov, rev, class)
							}
							if d.Fallback != FallbackPark {
								t.Errorf("%s/%s/%+v: a %s gap takes a default", preset, class, ov, class)
							}
						}
						if !master && d.Answerer == WhoMaster {
							t.Errorf("%s/%s/%+v: a master answers but there is none", preset, class, ov)
						}
						if preset == "ask-always" && ov == nil && d.Answerer != WhoUser {
							t.Errorf("ask-always/%s: %s answers", class, d.Answerer)
						}
					}
				}
			}
		}
	}
	if checked != 3*6*7*4 {
		t.Fatalf("checked %d combinations", checked)
	}
}

func TestAskOnRiskLetsTheModelSettleOnlyReversibleLowRiskGaps(t *testing.T) {
	s := DefaultSettings()
	for _, class := range []string{"ambiguity", "scope_growth", "verification_failed"} {
		if d := Decide(s, class, true, true); d.Answerer != WhoModel || d.Fallback != FallbackTakeDefault {
			t.Errorf("%s reversible = %+v", class, d)
		}
		if d := Decide(s, class, false, true); d.Answerer != WhoMaster {
			t.Errorf("%s irreversible = %+v", class, d)
		}
		if d := Decide(s, class, false, false); d.Answerer != WhoUser {
			t.Errorf("%s irreversible, no master = %+v", class, d)
		}
	}
	if d := Decide(s, "risky_action", true, true); d != (Decision{WhoMaster, FallbackPark}) {
		t.Errorf("risky_action = %+v", d)
	}
	if d := Decide(s, "missing_credential", true, false); d != (Decision{WhoUser, FallbackPark}) {
		t.Errorf("missing_credential without a master = %+v", d)
	}
}

func TestDecideAndLogStillAsksForRiskAndCredentials(t *testing.T) {
	s := DefaultSettings()
	s.AutonomyPreset = "decide-and-log"
	if d := Decide(s, "ambiguity", false, true); d.Answerer != WhoModel {
		t.Errorf("ambiguity = %+v", d)
	}
	for _, c := range []string{"risky_action", "missing_credential"} {
		if d := Decide(s, c, true, true); d.Answerer == WhoModel {
			t.Errorf("%s answered by the model under decide-and-log", c)
		}
	}
}

func TestOverrideAppliesToTheNamedClassOnly(t *testing.T) {
	s := DefaultSettings()
	s.AutonomyClasses = map[string]AutonomyChoice{"ambiguity": {Who: WhoUser, Fallback: FallbackPark}}
	if d := Decide(s, "ambiguity", true, true); d != (Decision{WhoUser, FallbackPark}) {
		t.Errorf("override ignored: %+v", d)
	}
	if d := Decide(s, "scope_growth", true, true); d.Answerer != WhoModel {
		t.Errorf("override leaked to another class: %+v", d)
	}
}

func TestSettingsWriteThatLetsTheModelAnswerRiskIsRefused(t *testing.T) {
	d := DefaultSettings()
	bad := map[string]string{
		"model answers risky":      `{"autonomy_classes": {"risky_action": {"who": "model", "fallback": "park"}}}`,
		"model answers credential": `{"autonomy_classes": {"missing_credential": {"who": "model", "fallback": "park"}}}`,
		"default for risky":        `{"autonomy_classes": {"risky_action": {"who": "master", "fallback": "take_default"}}}`,
		"default for credential":   `{"autonomy_classes": {"missing_credential": {"who": "user", "fallback": "take_default"}}}`,
		"unknown answerer":         `{"autonomy_classes": {"ambiguity": {"who": "anyone", "fallback": "park"}}}`,
		"unknown preset":           `{"autonomy_preset": "yolo"}`,
	}
	for name, patch := range bad {
		_, _, err := d.Apply([]byte(patch))
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, _, err := d.Apply([]byte(bad["model answers risky"]))
	if err == nil || !strings.Contains(err.Error(), "may not answer") {
		t.Errorf("the refusal says %v", err)
	}
	if _, _, err := d.Apply([]byte(`{"autonomy_classes": {"risky_action": {"who": "user", "fallback": "park"}, "ambiguity": {"who": "model", "fallback": "take_default"}}}`)); err != nil {
		t.Errorf("a valid matrix was refused: %v", err)
	}
}

func TestAutonomySettingsStayOperatorOnly(t *testing.T) {
	got := TouchesOperatorOnly([]string{"autonomy_preset", "autonomy_classes", "mesh_alias"})
	if len(got) != 2 {
		t.Fatalf("operator-only = %v", got)
	}
}
