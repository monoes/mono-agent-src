package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agenttokens"
)

func TestReplyAskToolIsServedAndGated(t *testing.T) {
	n := 0
	for _, tl := range allTools() {
		if tl.name == "session_reply_ask" {
			n++
			if !tl.mutating {
				t.Error("session_reply_ask must be mutating")
			}
		}
	}
	if n != 1 {
		t.Fatalf("session_reply_ask served %d times", n)
	}
}

func TestReplyAskToolAnswersOnlyForTheAddressedLead(t *testing.T) {
	m, _ := newAskFixture(t)
	t.Setenv(agenttokens.EnvMissionToken, "")
	out, err := m.as(t, m.tokM, "session_ask_ancestor", map[string]any{"question": "what is the budget?"})
	if err != nil {
		t.Fatal(err)
	}
	var asked struct {
		AskID int64 `json:"askId"`
	}
	if err := json.Unmarshal([]byte(out), &asked); err != nil || asked.AskID == 0 {
		t.Fatalf("%v %s", err, out)
	}
	args := map[string]any{"ask_id": asked.AskID, "message": "ten dollars"}
	if _, err := m.as(t, "", "session_reply_ask", args); err == nil || !strings.Contains(err.Error(), "no_session") {
		t.Fatalf("no token: %v", err)
	}
	if _, err := m.as(t, m.tokM, "session_reply_ask", args); err == nil || !strings.Contains(err.Error(), "not_yours") {
		t.Fatalf("the asker answered itself: %v", err)
	}
	if _, err := m.as(t, m.tokA, "session_reply_ask", args); err == nil || !strings.Contains(err.Error(), "not_yours") {
		t.Fatalf("a non-lead answered: %v", err)
	}
	for _, role := range []string{"worker", "verifier"} {
		if _, err := m.asBound(t, role, "session_reply_ask", args); err == nil {
			t.Fatalf("a bound %s answered", role)
		}
	}
	if n := commandsFor(t, m, m.master); n != 0 {
		t.Fatalf("refused replies queued %d", n)
	}
	t.Setenv(agenttokens.EnvMissionToken, "")
	out, err = m.as(t, m.tokOther, "session_reply_ask", args)
	if err != nil || !strings.Contains(out, "ask-r-") {
		t.Fatalf("%v %s", err, out)
	}
	if n := commandsFor(t, m, m.master); n != 1 {
		t.Fatalf("the asker got %d notes", n)
	}
	if _, err := m.as(t, m.tokOther, "session_reply_ask", args); err == nil || !strings.Contains(err.Error(), "invalid_input") {
		t.Fatalf("replay: %v", err)
	}
	if n := commandsFor(t, m, m.master); n != 1 {
		t.Fatalf("a replay queued a note: %d", n)
	}
}
