package bridge

import (
	"strings"
	"testing"
	"time"
)

// Launching a browser window is the operator's act: a browser command run from a coder turn (or
// any non-operator process) must not start one, and is told to start the browser itself. The
// operator's own terminal keeps the launch.
func TestEnsureConnected_LaunchGate(t *testing.T) {
	prevInst, prevRun, prevFind, prevStart := isExtensionInstalled, isChromeRunning, findLocalChromePath, startBrowser
	t.Cleanup(func() {
		isExtensionInstalled, isChromeRunning, findLocalChromePath, startBrowser = prevInst, prevRun, prevFind, prevStart
	})
	isExtensionInstalled = func() bool { return true }
	isChromeRunning = func() bool { return false }
	findLocalChromePath = func() string { return "/fake/browser" }
	stubOpener(t)

	cases := []struct {
		name    string
		refusal func() string
		launch  bool
	}{
		{"turn", func() string { return "this process runs inside a coder turn" }, false},
		{"no check wired", nil, false},
		{"operator", func() string { return "" }, true},
	}
	for _, c := range cases {
		launched := 0
		startBrowser = func(string) error { launched++; return nil }
		h := Hints{Route: func(ConnChecker) string { return "" }, RouteWaitPoll: time.Millisecond,
			Lifetime: func(ConnChecker) string { return "" }, Refusal: c.refusal}
		var err error
		out := captureStderr(t, func() { err = EnsureConnected(&fakePairingBridge{}, 20*time.Millisecond, h) })
		if c.launch != (launched == 1) || launched > 1 {
			t.Errorf("%s: launched %d times, want launch=%v", c.name, launched, c.launch)
		}
		if !c.launch {
			if err == nil || !strings.Contains(err.Error(), "start your browser") {
				t.Errorf("%s: want an actionable error telling the user to start the browser, got %v", c.name, err)
			}
			if strings.Contains(out, "launching it") {
				t.Errorf("%s: claimed to launch: %q", c.name, out)
			}
		}
	}
}
