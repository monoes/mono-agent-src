package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
)

// `browser permissions preset` sets every kind in one save and keeps the
// site rules; the GUI passes the preset after "--".
func TestBrowserPermissionsPreset(t *testing.T) {
	bt := newBrowserTest(t, false)
	bt.runJSON("permissions", "site", "--", "https://bank.example/", "block")

	// From the defaults (apply at ask): free puts apply at full, not left as it was.
	doc := bt.runJSON("permissions", "preset", "--", "free")
	l := doc["levels"].(map[string]any)
	for _, k := range browserctl.Kinds {
		want := browserctl.LevelFullNoApprovals
		if k == browserctl.KindApply {
			want = browserctl.LevelFull
		}
		if l[k] != want {
			t.Fatalf("after free: %s = %v, want %s (%v)", k, l[k], want, l)
		}
	}
	for _, c := range []struct{ preset, want string }{{"off", browserctl.LevelOff}, {"ask", browserctl.LevelFull}} {
		preset, want := c.preset, c.want
		doc = bt.runJSON("permissions", "preset", "--", preset)
		for _, k := range browserctl.Kinds {
			if doc["levels"].(map[string]any)[k] != want {
				t.Fatalf("after %s: %v", preset, doc["levels"])
			}
		}
		if sites := doc["sites"].([]any); len(sites) != 1 || sites[0].(map[string]any)["origin"] != "https://bank.example" {
			t.Fatalf("after %s, sites = %v", preset, sites)
		}
	}
	for _, bad := range []string{"full", "ASK", ""} {
		if _, err := bt.run("permissions", "preset", "--", bad); exitCodeFor(err) != 3 || jsonCode(err) != "invalid_input" {
			t.Fatalf("preset %q: %v (%s), want invalid_input", bad, err, jsonCode(err))
		}
	}
	if l := bt.runJSON("permissions")["levels"].(map[string]any); l["coder"] != browserctl.LevelFull {
		t.Fatalf("a refused preset changed the levels: %v", l)
	}
}

// `browser permissions` lists each preset's levels from browserctl, so the
// GUI needs no copy of them.
func TestBrowserPermissionsListsThePresets(t *testing.T) {
	bt := newBrowserTest(t, false)
	presets, ok := bt.runJSON("permissions")["presets"].(map[string]any)
	if !ok || len(presets) != 3 {
		t.Fatalf("presets = %v, want ask, free and off", presets)
	}
	for _, preset := range []string{browserctl.PresetAsk, browserctl.PresetFree, browserctl.PresetOff} {
		want, err := browserctl.PresetLevels(preset)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := presets[preset].(map[string]any)
		if len(got) != len(want) {
			t.Fatalf("presets[%s] = %v, want %v", preset, got, want)
		}
		for k, l := range want {
			if got[k] != l {
				t.Errorf("presets[%s][%s] = %v, want %s", preset, k, got[k], l)
			}
		}
	}
}

func TestBrowserPermissionsPresetIsTheUsers(t *testing.T) {
	bt := newBrowserTest(t, true)
	if _, err := bt.run("permissions", "preset", "free"); exitCodeFor(err) != 3 || jsonCode(err) != "operator_only" {
		t.Fatalf("agent preset: %v (%s)", err, jsonCode(err))
	}
}
