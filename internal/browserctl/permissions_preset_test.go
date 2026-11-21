package browserctl

import (
	"reflect"
	"testing"

	"github.com/monoes/mono-agent/internal/testdb"
)

func TestPresetLevels(t *testing.T) {
	want := map[string]map[string]string{
		PresetAsk: {KindCoder: LevelFull, KindApply: LevelFull, KindOrg: LevelFull, KindChat: LevelFull, KindOther: LevelFull},
		// apply can never be full-no-approvals: it stays at full.
		PresetFree: {KindCoder: LevelFullNoApprovals, KindApply: LevelFull, KindOrg: LevelFullNoApprovals, KindChat: LevelFullNoApprovals, KindOther: LevelFullNoApprovals},
		PresetOff:  {KindCoder: LevelOff, KindApply: LevelOff, KindOrg: LevelOff, KindChat: LevelOff, KindOther: LevelOff},
	}
	for preset, w := range want {
		got, err := PresetLevels(preset)
		if err != nil {
			t.Fatalf("PresetLevels(%q): %v", preset, err)
		}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("PresetLevels(%q) = %v, want %v", preset, got, w)
		}
		if len(got) != len(Kinds) {
			t.Errorf("PresetLevels(%q) has %d kinds, want %d", preset, len(got), len(Kinds))
		}
		var p Permissions
		for k, l := range got {
			if err := p.SetLevel(k, l); err != nil {
				t.Errorf("PresetLevels(%q): %s=%s fails validation: %v", preset, k, l, err)
			}
		}
		if _, _, err := encodePermissions(p); err != nil {
			t.Errorf("PresetLevels(%q) does not encode: %v", preset, err)
		}
	}
	// Each call returns its own map.
	a, _ := PresetLevels(PresetOff)
	a[KindCoder] = LevelFull
	if b, _ := PresetLevels(PresetOff); b[KindCoder] != LevelOff {
		t.Error("PresetLevels returned a shared map")
	}
}

func TestPresetLevels_Unknown(t *testing.T) {
	for _, preset := range []string{"", "full", "ASK", " ask"} {
		if got, err := PresetLevels(preset); err == nil {
			t.Errorf("PresetLevels(%q) = %v, want an error", preset, got)
		}
	}
}

// A preset sets every kind in one write and leaves the site rules alone.
func TestApplyPreset_WritesAllKindsOnce(t *testing.T) {
	db := testdb.Open(t).DB
	if _, err := UpdatePermissions(db, func(p *Permissions) error {
		if _, _, err := p.SetSite("https://github.com", RuleAllow); err != nil {
			return err
		}
		_, _, err := p.SetSite("bank.example", RuleBlock)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before, err := LoadPermissions(db)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	afterPermissionsRead = func() { writes++ }
	t.Cleanup(func() { afterPermissionsRead = nil })

	got, err := ApplyPreset(db, PresetOff)
	if err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Errorf("ApplyPreset wrote %d times, want 1", writes)
	}
	stored, err := LoadPermissions(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Permissions{got, stored} {
		for _, k := range Kinds {
			if p.Level(k) != LevelOff {
				t.Errorf("%s = %s after off, want off", k, p.Level(k))
			}
		}
		if !reflect.DeepEqual(p.Sites, before.Sites) {
			t.Errorf("site rules = %+v, want %+v unchanged", p.Sites, before.Sites)
		}
	}
	if _, err := ApplyPreset(db, "full"); err == nil {
		t.Error("ApplyPreset(full): want an error")
	}
	if p, _ := LoadPermissions(db); p.Level(KindCoder) != LevelOff {
		t.Errorf("a refused preset changed the setting: coder = %s", p.Level(KindCoder))
	}
}
