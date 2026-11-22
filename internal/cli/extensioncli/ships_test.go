package extensioncli

import "testing"

// Test harnesses, fixtures and the notes file stay in the repo, as they do in
// the store zip (scripts/package-extension-store.sh).
func TestShipsWithExtensionLeavesOutTestOnlyFiles(t *testing.T) {
	for name, want := range map[string]bool{
		"manifest.json":                 true,
		"background.js":                 true,
		"adapters/github.js":            true,
		"icons/icon16.png":              true,
		"gate.test.mjs":                 false,
		"adapters/arxiv.test.mjs":       false,
		"form_page.browser.test.mjs":    false,
		"extension_harness.mjs":         false,
		"browser_harness.mjs":           false,
		"recorder_session_harness.mjs":  false,
		"task_harness.mjs":              false,
		"highlight_page.fixtures.mjs":   false,
		"recorder_fake_dom.mjs":         false,
		"recorder_go_types.mjs":         false,
		"test_helpers.mjs":              false,
		"autopair_e2e.mjs":              false,
		"fixtures/page.html":            false,
		"adapters/fixtures/github.html": false,
		"icons/ICONS.txt":               false,
		"NOTICE":                        false,
	} {
		if got := shipsWithExtension(name); got != want {
			t.Errorf("shipsWithExtension(%q) = %v, want %v", name, got, want)
		}
	}
}
