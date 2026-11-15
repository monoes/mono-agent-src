package ai

import (
	"encoding/json"
	"errors"
	"fmt"
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

// A refusal may carry only a known failure code: a notice code of another family (the stop notice the open-ended run
// looks for, the coder banners) is refused and nothing is written.
func TestRecordFailedTurnNoticeRefusesCodesOfOtherFamilies(t *testing.T) {
	s, db := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	for _, code := range []string{NoticeOpenEndedStopped, "coder.background", "coder.workspace", "constructor", "x"} {
		if err := s.RecordFailedTurnNotice("p1", conv.ID, code, "reason"); !errors.Is(err, ErrUnknownRefusalCode) {
			t.Errorf("code %q: %v", code, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_turns`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("turns written by refused codes: %d %v", n, err)
	}
}

// The reason is cleaned before it is stored: control characters, ESC sequences' ESC, line breaks and bidi overrides go,
// and it is cut to the cap.
func TestRecordFailedTurnNoticeCleansAndCapsTheReason(t *testing.T) {
	s, _ := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	dirty := "bad\x1b[31m‮reason\r\nnext\x07 " + strings.Repeat("a", 3000)
	if err := s.RecordFailedTurnNotice("p1", conv.ID, "", dirty); err != nil {
		t.Fatal(err)
	}
	turns, _, _ := s.ListTurns(conv.ID, "p1", "", 10)
	r := turns[0].Reason
	if strings.ContainsAny(r, "\x1b\r\n\x07‮") || len([]rune(r)) > MaxRefusalReasonRunes+1 || !strings.HasPrefix(r, "bad[31mreason next") {
		t.Fatalf("reason stored: %q", r[:60])
	}
	if err := s.RecordFailedTurnNotice("p1", conv.ID, "", "\x1b\x07 "); err == nil {
		t.Error("accepted a reason that is only control characters")
	}
}

// A conversation keeps the newest MaxRefusalNotices refusals (the oldest go with their events), and a refusal is not a
// turn of the work.
func TestRecordFailedTurnNoticeKeepsTheNewestAndIsNotAWorkTurn(t *testing.T) {
	s, db := newChatEventStore(t)
	conv, _ := s.CreateConversation("p1", "agent", "general", "claude", "", "")
	for i := 0; i < MaxRefusalNotices+5; i++ {
		if err := s.RecordFailedTurnNotice("p1", conv.ID, "", fmt.Sprintf("reason-%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	turns, _, err := s.ListTurns(conv.ID, "p1", "", 100)
	if err != nil || len(turns) != MaxRefusalNotices {
		t.Fatalf("kept %d turns: %v", len(turns), err)
	}
	have := map[string]bool{}
	for _, tr := range turns {
		have[tr.Reason] = true
	}
	if !have["reason-24"] || !have["reason-05"] || have["reason-04"] || have["reason-00"] {
		t.Fatalf("not the newest kept: %v", have)
	}
	var orphans int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_chat_events WHERE turn_id NOT IN (SELECT id FROM ai_chat_turns)`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("events left behind: %d %v", orphans, err)
	}
	if had, err := s.HasWorkTurn("p1", conv.ID); err != nil || had {
		t.Fatalf("a refusal counted as a work turn: %v %v", had, err)
	}
	if _, _, err := s.CreateTurn(conv.ID, "p1", "t-1", "inst", "hello"); err != nil {
		t.Fatal(err)
	}
	if had, err := s.HasWorkTurn("p1", conv.ID); err != nil || !had {
		t.Fatalf("a real turn not counted: %v %v", had, err)
	}
}
