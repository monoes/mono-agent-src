package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/account/accounttest"
)

func TestDetectChromiumBrowsers_ListsAllSeven(t *testing.T) {
	got, err := (&App{}).DetectChromiumBrowsers()
	if err != nil {
		t.Fatal(err)
	}
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
		if err := a.OpenChromiumBrowser(id); err == nil || account.IsLoginRequired(err) {
			t.Errorf("OpenChromiumBrowser(%q) = %v, want the unknown-browser error", id, err)
		}
	}
}

// Signed out, both bindings answer with the login-required error, so the
// step shows the sign-in message rather than an empty list.
func TestChromiumBindings_SignedOutSayLoginRequired(t *testing.T) {
	accounttest.Install(t, accounttest.LockedNoLogin)
	a := newTestApp(t)
	if got, err := a.DetectChromiumBrowsers(); !account.IsLoginRequired(err) || got != nil {
		t.Errorf("DetectChromiumBrowsers signed out = %v, %v; want nil and the login-required error", got, err)
	}
	if err := a.OpenChromiumBrowser("chrome"); !account.IsLoginRequired(err) {
		t.Errorf("OpenChromiumBrowser signed out = %v, want the login-required error", err)
	}
}
