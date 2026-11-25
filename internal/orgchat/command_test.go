package orgchat

import (
	"strings"
	"testing"
)

// #1766: a person can only approve a Bash call when the item says which command.
func TestApprovalItemsCarryTheCommand(t *testing.T) {
	cut := strings.Repeat("a", 200) + "…" // monomind's cut of a longer command
	events := []Event{
		{ID: "e1", TS: 1, Type: "question", From: "writer", Data: map[string]interface{}{
			"question": "Approval required for Bash", "action": "Bash", "requestId": "apr-1",
			"input": map[string]interface{}{"command": "printf hi > /tmp/note.txt"},
		}},
		// An old bus event with no input: the item list still has it.
		{ID: "e2", TS: 2, Type: "question", From: "dev", Data: map[string]interface{}{"action": "Bash", "requestId": "apr-2"}},
	}
	human, err := ParseApprovals([]byte(`{"items":[
		{"roleId":"dev","action":"Bash","ts":2,"approved":null,"requestId":"apr-2","input":{"command":"` + cut + `"}},
		{"roleId":"qa","action":"Bash","ts":3,"approved":null,"requestId":"apr-3","input":{"command":"go test ./..."}},
		{"roleId":"qa","action":"WebFetch","ts":4,"approved":null,"requestId":"apr-4","input":{"url":"https://example.com"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Item{}
	for _, it := range BuildThread("acme", "ceo", events, HumanItems{Approvals: human}, 0) {
		got[it.Ref] = it
	}
	for ref, want := range map[string]struct {
		command   string
		truncated bool
	}{
		"apr-1": {"printf hi > /tmp/note.txt", false},
		"apr-2": {cut, true},
		"apr-3": {"go test ./...", false},
		"apr-4": {"", false},
	} {
		it := got[ref]
		if it.Command != want.command || it.CommandTruncated != want.truncated {
			t.Errorf("%s: command %q truncated %v, want %q %v", ref, it.Command, it.CommandTruncated, want.command, want.truncated)
		}
	}
}

func TestCommandTruncationIsMonomindsCutOnly(t *testing.T) {
	for name, tc := range map[string]struct {
		command string
		want    bool
	}{
		"exactly the limit":        {strings.Repeat("a", 200), false},
		"a command ending in …":    {"echo wait…", false},
		"cut":                      {strings.Repeat("a", 200) + "…", true},
		"cut, counted as monomind": {strings.Repeat("a", 198) + "😀…", true}, // the emoji is two UTF-16 units
	} {
		_, truncated := Command(map[string]interface{}{"command": tc.command})
		if truncated != tc.want {
			t.Errorf("%s: truncated = %v, want %v", name, truncated, tc.want)
		}
	}
}
