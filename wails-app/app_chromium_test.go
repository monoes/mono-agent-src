package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/account"
)

func TestDetectChromiumBrowsers_ListsAllSeven(t *testing.T) {
	got := (&App{}).DetectChromiumBrowsers()
	if len(got) != 7 {
		t.Fatalf("DetectChromiumBrowsers = %d browsers, want 7: %+v", len(got), got)
	}
	seen := map[string]bool{}
	for _, b := range got {
		seen[b.ID] = true
	}
	for _, id := range []string{"chrome", "edge", "brave", "arc", "vivaldi", "opera", "chromium"} {
		if !seen[id] {
			t.Errorf("%s missing from %+v", id, got)
		}
	}
}

// Only refused ids: nothing may launch a browser in a test. A path is never
// accepted in place of an id.
func TestOpenChromiumBrowser_RefusesUnknownIDs(t *testing.T) {
	a := &App{}
	for _, id := range []string{"nope", "../x", "/Applications/Google Chrome.app", ""} {
		err := a.OpenChromiumBrowser(id)
		if err == nil {
			t.Errorf("OpenChromiumBrowser(%q): want an error", id)
			continue
		}
		if account.IsLoginRequired(err) {
			t.Fatalf("OpenChromiumBrowser(%q) was refused by the account gate, not the id check: %v", id, err)
		}
	}
}
