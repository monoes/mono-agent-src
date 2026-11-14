package coderhost

import (
	"encoding/json"
	"math/rand"
	"testing"
)

func TestSettingsDigestIsStableAndMovesWithAnySetting(t *testing.T) {
	a, b := DefaultSettings(), DefaultSettings()
	if SettingsDigest(a) != SettingsDigest(b) || len(SettingsDigest(a)) != 64 {
		t.Fatalf("the digest of equal settings differs or is not a sha256 hex: %q", SettingsDigest(a))
	}
	a.AutonomyClasses = map[string]AutonomyChoice{"ambiguity": {Who: WhoUser, Fallback: FallbackPark}, "scope_growth": {Who: WhoModel, Fallback: FallbackTakeDefault}}
	b.AutonomyClasses = map[string]AutonomyChoice{"scope_growth": {Who: WhoModel, Fallback: FallbackTakeDefault}, "ambiguity": {Who: WhoUser, Fallback: FallbackPark}}
	if SettingsDigest(a) != SettingsDigest(b) {
		t.Error("map insertion order moved the digest")
	}
	c := a
	c.AutonomyPreset = "decide-and-log"
	if SettingsDigest(a) == SettingsDigest(c) {
		t.Error("a changed preset kept the digest")
	}
}

func randomAutonomy(r *rand.Rand) (preset string, classes map[string]AutonomyChoice) {
	preset = AutonomyPresets[r.Intn(len(AutonomyPresets))]
	classes = map[string]AutonomyChoice{}
	for _, c := range GapClasses {
		if r.Intn(3) == 0 {
			classes[c] = AutonomyChoice{Who: []string{WhoUser, WhoMaster, WhoModel}[r.Intn(3)], Fallback: []string{FallbackPark, FallbackTakeDefault}[r.Intn(2)]}
		}
	}
	return
}

// The app judges an autonomy patch with AutonomyAfterPatch; the CLI stores what Settings.Apply makes of the same patch. They must
// never drift: every random baseline and patch gives the same decision for every kind of question, and the same verdict.
func TestAutonomyAfterPatchAgreesWithApply(t *testing.T) {
	r := rand.New(rand.NewSource(1633))
	judged := 0
	for i := 0; i < 3000; i++ {
		cur := DefaultSettings()
		cur.AutonomyPreset, cur.AutonomyClasses = randomAutonomy(r)
		patch := map[string]json.RawMessage{}
		if r.Intn(3) != 0 {
			p, _ := randomAutonomy(r)
			patch["autonomy_preset"], _ = json.Marshal(p)
			if r.Intn(8) == 0 {
				patch["autonomy_preset"] = json.RawMessage(`null`)
			}
		}
		if r.Intn(3) != 0 || len(patch) == 0 {
			_, c := randomAutonomy(r)
			patch["autonomy_classes"], _ = json.Marshal(c)
			if r.Intn(6) == 0 {
				patch["autonomy_classes"] = json.RawMessage(`null`)
			}
		}
		raw, _ := json.Marshal(patch)
		want, _, err := cur.Apply(raw)
		if err != nil { // a patch the host refuses (a hard class given to the model) is never stored: nothing to judge
			continue
		}
		judged++
		got, err := AutonomyAfterPatch(cur, patch)
		if err != nil {
			t.Fatalf("case %d: the helper refused %s: %v", i, raw, err)
		}
		if AutonomyWidens(want, got) || AutonomyWidens(got, want) {
			t.Fatalf("case %d: the helper and Apply decide differently for baseline %q %v and patch %s", i, cur.AutonomyPreset, cur.AutonomyClasses, raw)
		}
		if AutonomyWidens(cur, got) != AutonomyWidens(cur, want) {
			t.Fatalf("case %d: different verdicts for patch %s", i, raw)
		}
	}
	if judged < 500 {
		t.Fatalf("only %d of 3000 random patches were valid: the generator no longer covers the judgement", judged)
	}
}

func TestAutonomyAfterPatchRefusesWhatApplyRefuses(t *testing.T) {
	for _, p := range []string{`{"autonomy_preset":5}`, `{"autonomy_classes":[1]}`, `{"autonomy_classes":{"ambiguity":5}}`} {
		var keys map[string]json.RawMessage
		_ = json.Unmarshal([]byte(p), &keys)
		if _, err := AutonomyAfterPatch(DefaultSettings(), keys); err == nil {
			t.Errorf("the helper took %s", p)
		}
	}
}

func TestAutonomyAfterPatchNullClassesReplaceByAnEmptyMap(t *testing.T) {
	cur := DefaultSettings()
	cur.AutonomyPreset = "decide-and-log"
	cur.AutonomyClasses = map[string]AutonomyChoice{"ambiguity": {Who: WhoUser, Fallback: FallbackPark}}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"autonomy_classes":null}`), &keys) // null replaces the map by an empty one
	next, err := AutonomyAfterPatch(cur, keys)
	if err != nil || len(next.AutonomyClasses) != 0 || !AutonomyWidens(cur, next) {
		t.Fatalf("null classes: %v %v", next.AutonomyClasses, err)
	}
}
