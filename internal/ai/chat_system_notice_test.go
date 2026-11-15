package ai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// A system notice is a finished turn of its own: no prompt, one notice, one turn.finished. It never makes the
// conversation busy, so it is written even while another instance's turn is active.
func TestRecordSystemNoticeIsAFinishedPromptlessTurnWithOneNotice(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, err := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	// another instance's turn is running: a normal CreateTurn from a different owner would be refused
	if _, _, err := s.CreateTurn(conv.ID, "p1", "live-turn", "other-instance", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordSystemNotice("p1", conv.ID, "doomsday.stopped", "Open-ended run stopped by you at T: stopped"); err != nil {
		t.Fatalf("RecordSystemNotice: %v", err)
	}
	turns, _, err := s.ListTurns(conv.ID, "p1", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	var sys *Turn
	for i := range turns {
		if turns[i].ID != "live-turn" {
			sys = &turns[i]
		}
	}
	if sys == nil || sys.Prompt != "" || sys.Status != "completed" || !strings.HasPrefix(sys.ID, "system-") {
		t.Fatalf("system turn: %+v", sys)
	}
	evs, err := s.GetEvents(conv.ID, sys.ID, "p1", 0, 10)
	if err != nil || len(evs) != 2 || evs[0].Type != chatevents.EventNotice || evs[1].Type != chatevents.EventTurnFinished {
		t.Fatalf("events: %+v %v", evs, err)
	}
	var n chatevents.NoticePayload
	if err := json.Unmarshal(evs[0].Payload, &n); err != nil || n.Code != "doomsday.stopped" || n.Message != "Open-ended run stopped by you at T: stopped" {
		t.Fatalf("notice: %+v %v", n, err)
	}
}

// Another profile's conversation, a missing one and an empty code or message are refused; nothing is written.
func TestRecordSystemNoticeRefusesWhatIsNotTheCallersConversation(t *testing.T) {
	s, db := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	for _, c := range []struct{ profile, conv, code, msg string }{
		{"p2", conv.ID, "c", "m"}, {"p1", "nope", "c", "m"}, {"p1", conv.ID, "", "m"}, {"p1", conv.ID, "c", ""}, {"", conv.ID, "c", "m"},
	} {
		if err := s.RecordSystemNotice(c.profile, c.conv, c.code, c.msg); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_turns`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("turns written by refused calls: %d %v", n, err)
	}
}

// A turn that was refused before it began is written as a finished system turn that says why: the turn is failed with
// the reason, its notice is an error carrying the reason, and turn.finished carries status, reason and code, so a
// reopened conversation shows what failed.
func TestRecordFailedTurnNoticeKeepsTheReasonAndCode(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, err := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordFailedTurnNotice("p1", conv.ID, "agent_not_setup", "the coder host could not start the turn"); err != nil {
		t.Fatalf("RecordFailedTurnNotice: %v", err)
	}
	turns, _, err := s.ListTurns(conv.ID, "p1", "", 10)
	if err != nil || len(turns) != 1 {
		t.Fatalf("turns: %+v %v", turns, err)
	}
	if turns[0].Status != "failed" || turns[0].Reason != "the coder host could not start the turn" || !turns[0].IsSystem() {
		t.Fatalf("system turn: %+v", turns[0])
	}
	evs, err := s.GetEvents(conv.ID, turns[0].ID, "p1", 0, 10)
	if err != nil || len(evs) != 2 {
		t.Fatalf("events: %+v %v", evs, err)
	}
	var n chatevents.NoticePayload
	if err := json.Unmarshal(evs[0].Payload, &n); err != nil || n.Severity != chatevents.SeverityError || n.Message != "the coder host could not start the turn" {
		t.Fatalf("notice: %+v %v", n, err)
	}
	var f chatevents.TurnFinishedPayload
	if err := json.Unmarshal(evs[1].Payload, &f); err != nil || f.Status != chatevents.StatusFailed || f.Reason == "" || f.Code != "agent_not_setup" {
		t.Fatalf("turn.finished: %+v %v", f, err)
	}
	if err := s.RecordFailedTurnNotice("p1", conv.ID, "", " "); err == nil {
		t.Error("accepted an empty reason")
	}
}
