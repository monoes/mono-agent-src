//go:build !windows

package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

const missionSessionDoc = `{"profile":{"id":"work","name":"Work"},"session":{"id":7,"conversation_id":"stored-conversation","state":"running","rev":2}}`

func TestApprovingOrResumingTaskSessionStartsItsStoredConversation(t *testing.T) {
	for _, verb := range []string{"approve", "resume"} {
		t.Run(verb, func(t *testing.T) {
			taskCLIFake(t, missionSessionDoc, 0)
			a := newTaskTestApp(t)
			sup, _, q := newTestSupervisor(t)
			a.chatSup = sup
			t.Cleanup(sup.stopAll)
			proc := newFakeChatProcess("")
			q.push(proc)
			go proc.writeLine(admissionLine("stored-conversation", "mission-7-2", "active", false))
			var raw string
			if verb == "approve" {
				raw = a.TaskSessionApprove(7)
			} else {
				raw = a.TaskSessionResume(7)
			}
			var result struct {
				Error        string
				Continuation struct {
					TurnID string `json:"turnId"`
				}
			}
			if err := json.Unmarshal([]byte(raw), &result); err != nil || result.Error != "" || result.Continuation.TurnID != "mission-7-2" {
				t.Fatalf("continuation: %s %v", raw, err)
			}
			q.mu.Lock()
			args := strings.Join(q.argvs[0], " ")
			q.mu.Unlock()
			if !strings.Contains(args, "--profile work --json chat --conversation stored-conversation --turn mission-7-2") || !strings.Contains(args, "Continue the approved mission session #7") {
				t.Fatalf("wrong continuation: %s", args)
			}
			go func() {
				proc.writeLine(eventLine("stored-conversation", "mission-7-2", 1, chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{Status: chatevents.StatusCompleted, HistorySaved: true}))
				proc.endStream(nil)
			}()
			waitReleased(t, sup, "stored-conversation", "mission-7-2")
		})
	}
}

func TestSessionContinuationWaitsForPlanningAndQueuesOnlyOnce(t *testing.T) {
	sup, _, q := newTestSupervisor(t)
	t.Cleanup(sup.stopAll)
	a := &App{chatSup: sup}
	if _, _, err := sup.admit("stored-conversation", "planning"); err != nil {
		t.Fatal(err)
	}
	proc := newFakeChatProcess("")
	q.push(proc)
	for i := 0; i < 2; i++ {
		raw := a.continueTaskSession(missionSessionDoc)
		if !strings.Contains(raw, `"status":"queued"`) {
			t.Fatalf("not queued: %s", raw)
		}
	}
	q.mu.Lock()
	n := len(q.argvs)
	q.mu.Unlock()
	if n != 0 {
		t.Fatal("started before planning ended")
	}
	a.selectForTest("another-profile") // the queued turn retains its admission profile
	go proc.writeLine(admissionLine("stored-conversation", "mission-7-2", "active", false))
	sup.release("stored-conversation", "planning")
	q.mu.Lock()
	n = len(q.argvs)
	q.mu.Unlock()
	if n != 1 {
		t.Fatalf("started %d continuations, want 1", n)
	}
	q.mu.Lock()
	args := strings.Join(q.argvs[0], " ")
	q.mu.Unlock()
	if !strings.HasPrefix(args, "--profile work ") {
		t.Fatalf("queued turn changed profile: %s", args)
	}
	go func() {
		proc.writeLine(eventLine("stored-conversation", "mission-7-2", 1, chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{Status: chatevents.StatusCompleted, HistorySaved: true}))
		proc.endStream(nil)
	}()
	waitReleased(t, sup, "stored-conversation", "mission-7-2")
}

func TestSessionContinuationStopsWithThePlanningTurn(t *testing.T) {
	sup, _, q := newTestSupervisor(t)
	a := &App{chatSup: sup}
	h, _, err := sup.admit("stored-conversation", "planning")
	if err != nil {
		t.Fatal(err)
	}
	a.continueTaskSession(missionSessionDoc)
	h.requestStop()
	sup.release("stored-conversation", "planning")
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.argvs) != 0 {
		t.Fatal("stopped planning launched another turn")
	}
}

// Even a callback extracted by release just before stopAll cannot admit a new turn.
func TestSessionContinuationCannotStartAfterShutdown(t *testing.T) {
	sup, _, q := newTestSupervisor(t)
	a := &App{chatSup: sup}
	h, _, err := sup.admit("stored-conversation", "planning")
	if err != nil {
		t.Fatal(err)
	}
	a.continueTaskSession(missionSessionDoc)
	sup.mu.Lock()
	after := sup.afterConversation["stored-conversation"]
	delete(sup.afterConversation, "stored-conversation")
	sup.mu.Unlock()
	// No process was launched for this admitted planning handle.
	sup.release(h.conversationID, h.turnID)
	sup.stopAll()
	after()
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.argvs) != 0 {
		t.Fatal("an extracted continuation started after shutdown")
	}
}

func TestQueuedContinuationFailureReachesTheConversation(t *testing.T) {
	sup, emitter, q := newTestSupervisor(t)
	t.Cleanup(sup.stopAll)
	a := &App{chatSup: sup}
	if _, _, err := sup.admit("stored-conversation", "planning"); err != nil {
		t.Fatal(err)
	}
	proc := newFakeChatProcess("")
	q.push(proc)
	if raw := a.continueTaskSession(missionSessionDoc); !strings.Contains(raw, `"status":"queued"`) {
		t.Fatalf("not queued: %s", raw)
	}
	go proc.endStream(errors.New("boom")) // the process dies before admitting the turn
	sup.release("stored-conversation", "planning")
	var got *chatevents.Event
	for _, ev := range emitter.snapshot() {
		if ev.Type == chatevents.EventNotice {
			ev := ev
			got = &ev
		}
	}
	if got == nil || got.ConversationID != "stored-conversation" {
		t.Fatalf("no notice for the conversation: %+v", emitter.snapshot())
	}
	var p chatevents.NoticePayload
	if err := json.Unmarshal(got.Payload, &p); err != nil || p.Code != "mission.continue_failed" || p.Severity != chatevents.SeverityError || !strings.Contains(p.Message, "#7") {
		t.Fatalf("notice payload: %+v %v", p, err)
	}
}
