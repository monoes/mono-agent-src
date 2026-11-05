package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// A browser command from a process inside a coder turn whose markers were cleared gets the agents'
// limits, not the user's unrestricted level: the same origin policy as a marked agent, a tab list
// confined to the agent's own, and permissions that name it as an agent.
func TestBrowserCommandsFromAnUnmarkedTurnAreHeldToTheAgentPolicy(t *testing.T) {
	bt := newBrowserTest(t, false) // no marker at all
	opgate.SetForTest(t, func(bool) string { return "this command runs inside a coder session's turn" })

	_, err := bt.run("open", "https://example.com/")
	if exitCodeFor(err) != 3 || jsonCode(err) != "origin_not_allowed" {
		t.Fatalf("open example.com from a turn: exit %d code %q: %v", exitCodeFor(err), jsonCode(err), err)
	}
	if len(bt.fake.Calls()) != 0 {
		t.Fatalf("a refused open reached the browser: %v", bt.fake.Types())
	}
	bt.fake.AddTab("https://mail.example/inbox", "", true)
	if tabs := bt.runJSON("tabs")["tabs"].([]any); len(tabs) != 0 {
		t.Fatalf("a turn sees %d tabs of the user's", len(tabs))
	}
	you := bt.runJSON("permissions")["you"].(map[string]any)
	if you["kind"] != "other" || you["level"] == "unrestricted" {
		t.Fatalf("permissions name a turn as %v", you)
	}
}

// The user's own terminal outside a turn is not restricted.
func TestBrowserCommandsFromTheUsersTerminalOutsideATurnAreUnrestricted(t *testing.T) {
	bt := newBrowserTest(t, false)
	opgate.SetForTest(t, func(bool) string { return "" })
	bt.runJSON("open", "https://example.com/")
	if you := bt.runJSON("permissions")["you"].(map[string]any); you["kind"] != "human" {
		t.Fatalf("permissions name the user as %v", you)
	}
}
