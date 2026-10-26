//go:build !windows

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

func hostTestTiming(t *testing.T) {
	t.Helper()
	p, i, a := hostRelayPoll, hostRelayIdlePoll, hostRelayIdleAfter
	hostRelayPoll, hostRelayIdlePoll = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { hostRelayPoll, hostRelayIdlePoll, hostRelayIdleAfter = p, i, a })
}

func coderShowReply(t *testing.T, conversationID string, access string) fakeChatReply {
	t.Helper()
	b, err := json.Marshal(ai.Conversation{ID: conversationID, Access: access, Mode: ai.ModeCoder}.Record())
	if err != nil {
		t.Fatal(err)
	}
	return fakeChatReply{match: "chat history show", stdout: string(b)}
}

func eventsReply(t *testing.T, conversationID, turnID string, evs ...chatevents.Event) fakeChatReply {
	t.Helper()
	recs := []chatevents.Record{}
	for _, e := range evs {
		recs = append(recs, e.Record())
	}
	b, _ := json.Marshal(map[string]any{"items": recs, "last_committed_seq": len(recs), "has_more": false})
	return fakeChatReply{match: "chat history events", stdout: string(b)}
}

func hostEvent(t *testing.T, conv, turn string, seq int64, typ chatevents.EventType, payload any) chatevents.Event {
	t.Helper()
	ev, err := chatevents.New("default", conv, turn, seq, time.Now(), typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func waitFinished(t *testing.T, em *collectingEmitter) chatevents.Event {
	t.Helper()
	for i := 0; i < 400; i++ {
		if got := em.byType(chatevents.EventTurnFinished); len(got) > 0 {
			return got[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no turn.finished was emitted")
	return chatevents.Event{}
}

// A coder conversation's turn goes to the host as a command and the app follows
// its journal; it does not start a `chat` process of its own.
func TestStartChatTurnSendsACoderTurnToTheHostAndFollowsItsJournal(t *testing.T) {
	hostTestTiming(t)
	fin := hostEvent(t, "c1", "t1", 2, chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{Status: chatevents.StatusCompleted, HistorySaved: true})
	bin, argsLog := chatFakeCLI(t,
		coderShowReply(t, "c1", ai.AccessCoding),
		fakeChatReply{match: "coder sessions send", stdout: `{"commandId":7,"conversationId":"c1","turnId":"t1"}`},
		eventsReply(t, "c1", "t1", fin),
	)
	a, em := newCLIChatApp(t, bin)
	r := decodeStart(t, a.StartChatTurn("c1", "t1", "-- do it", false, false, nil))
	if !r.OK || r.Status != "active" {
		t.Fatalf("start = %+v", r)
	}
	if got := waitFinished(t, em); got.Seq != 2 {
		t.Fatalf("finished event = %+v", got)
	}
	log := strings.Join(readArgsLog(t, argsLog), "\n")
	if !strings.Contains(log, "coder sessions send --turn t1 -- c1 -- do it") {
		t.Fatalf("send call missing or message not after --: %s", log)
	}
	if strings.Contains(log, " chat --conversation") {
		t.Fatalf("the app started a turn process of its own: %s", log)
	}
	// the slot is released once the turn finished, so the next turn is admitted
	time.Sleep(50 * time.Millisecond)
	if _, already, err := a.chatSup.admit("c1", "t2"); err != nil || already {
		t.Fatalf("slot after the turn: already=%v err=%v", already, err)
	}
}

// CoderConfirmMission on a coder conversation reaches the host as the send
// command's own --mission flag, before the "--"; a plain send and a message that
// only says so never carry it.
func TestCoderConfirmMissionPassesMissionFlagToTheHostSend(t *testing.T) {
	hostTestTiming(t)
	fin := hostEvent(t, "c1", "t1", 2, chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{Status: chatevents.StatusCompleted, HistorySaved: true})
	bin, argsLog := chatFakeCLI(t,
		coderShowReply(t, "c1", ai.AccessCoding),
		fakeChatReply{match: "coder sessions send", stdout: `{"commandId":7,"conversationId":"c1","turnId":"t1"}`},
		eventsReply(t, "c1", "t1", fin),
	)
	a, em := newCLIChatApp(t, bin)
	if r := decodeStart(t, a.CoderConfirmMission("c1", "t1", "yes, start")); !r.OK {
		t.Fatalf("start = %+v", r)
	}
	waitFinished(t, em)
	log := strings.Join(readArgsLog(t, argsLog), "\n")
	if !strings.Contains(log, "coder sessions send --turn t1 --mission -- c1 yes, start") {
		t.Fatalf("mission flag missing or after --: %s", log)
	}
	// a plain send of the same words carries no flag
	time.Sleep(50 * time.Millisecond)
	if r := decodeStart(t, a.StartChatTurn("c1", "t2", "yes, start the mission --mission", false, false, nil)); !r.OK {
		t.Fatalf("start = %+v", r)
	}
	log = strings.Join(readArgsLog(t, argsLog), "\n")
	if strings.Contains(log, "sessions send --turn t2 --mission") {
		t.Fatalf("a plain send carried the mission flag: %s", log)
	}
}

// A send the host refuses (no turn is ever created) ends the turn in the UI with the reason.
func TestAHostSendThatFailsEndsTheTurnWithItsReason(t *testing.T) {
	hostTestTiming(t)
	bin, _ := chatFakeCLI(t,
		coderShowReply(t, "c1", ai.AccessCoding),
		fakeChatReply{match: "coder sessions send", stdout: `{"commandId":9}`},
		fakeChatReply{match: "chat history events", stderr: "turn not found", code: 2},
		fakeChatReply{match: "coder sessions command 9", stdout: `{"id":9,"status":"failed","error":"chat: conversation has an active turn owned by another instance"}`},
	)
	a, em := newCLIChatApp(t, bin)
	if r := decodeStart(t, a.StartChatTurn("c1", "t1", "hi", false, false, nil)); !r.OK {
		t.Fatalf("start = %+v", r)
	}
	ev := waitFinished(t, em)
	var p chatevents.TurnFinishedPayload
	_ = json.Unmarshal(ev.Payload, &p)
	if p.Status != chatevents.StatusFailed || !strings.Contains(p.Reason, "owned by another instance") || ev.Seq != chatevents.MaxSafeSeq {
		t.Fatalf("finish = %+v seq %d", p, ev.Seq)
	}
}

// A scoped chat keeps the app's own process; the host is not involved.
func TestStartChatTurnKeepsScopedChatsOffTheHost(t *testing.T) {
	bin, argsLog := chatFakeCLI(t,
		coderShowReply(t, "c1", ai.AccessScoped),
		fakeChatReply{match: " chat --conversation", stdout: `{"admitted":true,"existed":false,"turn":{"id":"t1","status":"active"}}` + "\n"},
	)
	a, _ := newCLIChatApp(t, bin)
	decodeStart(t, a.StartChatTurn("c1", "t1", "hi", false, false, nil))
	if log := strings.Join(readArgsLog(t, argsLog), "\n"); strings.Contains(log, "coder sessions") {
		t.Fatalf("a scoped chat was sent to the host: %s", log)
	}
}

// Stop goes to the host, and closing the app stops following without stopping the turn.
func TestStopGoesToTheHostAndShutdownLeavesItsTurnRunning(t *testing.T) {
	hostTestTiming(t)
	bin, argsLog := chatFakeCLI(t,
		coderShowReply(t, "c1", ai.AccessCoding),
		fakeChatReply{match: "coder sessions send", stdout: `{"commandId":7}`},
		fakeChatReply{match: "coder sessions stop", stdout: `{"commandId":8}`},
		eventsReply(t, "c1", "t1"), // no events yet: the turn keeps running
	)
	a, _ := newCLIChatApp(t, bin)
	if r := decodeStart(t, a.StartChatTurn("c1", "t1", "hi", false, false, nil)); !r.OK {
		t.Fatalf("start = %+v", r)
	}
	if out := a.StopChatTurn("c1", "t1"); !strings.Contains(out, `"ok":true`) {
		t.Fatalf("stop = %s", out)
	}
	if log := strings.Join(readArgsLog(t, argsLog), "\n"); !strings.Contains(log, "coder sessions stop c1") {
		t.Fatalf("no stop for the host: %s", log)
	}
	n := strings.Count(strings.Join(readArgsLog(t, argsLog), "\n"), "coder sessions stop")
	a.chatSup.stopAll()
	if got := strings.Count(strings.Join(readArgsLog(t, argsLog), "\n"), "coder sessions stop"); got != n {
		t.Fatalf("app shutdown stopped the host's turn (%d stops, was %d)", got, n)
	}
}

func TestHostOwnedTurnsAreNotForeign(t *testing.T) {
	turn := ai.Turn{Status: "active", OwnerInstanceID: "host-1a2b3c4d"}
	if isForeignActiveTurn(turn, "me") {
		t.Fatal("a turn the host runs was shown as another window's")
	}
	turn.OwnerInstanceID = "someone-else"
	if !isForeignActiveTurn(turn, "me") {
		t.Fatal("another window's turn is no longer foreign")
	}
}

// Reopening the app finds the host's active turn and follows it again.
func TestGetChatTurnsFollowsAnActiveHostTurn(t *testing.T) {
	hostTestTiming(t)
	fin := hostEvent(t, "c1", "t1", 5, chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{Status: chatevents.StatusCompleted, HistorySaved: true})
	turns, _ := json.Marshal(map[string]any{"items": []ai.TurnRecord{{ID: "t1", ConversationID: "c1", OwnerInstanceID: "host-1a2b3c4d", Status: "active", LastCommittedSeq: 4}}})
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "chat history turns", stdout: string(turns)},
		eventsReply(t, "c1", "t1", fin),
	)
	a, em := newCLIChatApp(t, bin)
	out := a.GetChatTurns("c1", "", 20)
	if !strings.Contains(out, `"ownedByThisInstance":true`) {
		t.Fatalf("host turn listed as foreign: %s", out)
	}
	waitFinished(t, em)
	if log := strings.Join(readArgsLog(t, argsLog), "\n"); !strings.Contains(log, "--after-seq 4") {
		t.Fatalf("followed from the start instead of the last committed event: %s", log)
	}
}
