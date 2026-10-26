package main

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// continueTaskSession runs only after the CLI accepted the operator's approve
// or resume. The stored session supplies the conversation; callers cannot name
// a different chat. Manual sessions without a conversation need no lead turn.
func (a *App) continueTaskSession(raw string) string {
	var doc struct {
		Profile struct {
			ID string `json:"id"`
		} `json:"profile"`
		Session struct {
			ID             int64  `json:"id"`
			ConversationID string `json:"conversation_id"`
			State          string `json:"state"`
			Rev            int64  `json:"rev"`
		} `json:"session"`
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(raw), &doc) != nil || doc.Error != "" || doc.Session.State != "running" || doc.Session.ConversationID == "" || doc.Profile.ID == "" {
		return raw
	}
	var result map[string]any
	if json.Unmarshal([]byte(raw), &result) != nil {
		return raw
	}
	if a.chatSup == nil {
		result["error"] = "The session is approved, but its lead could not start: chat supervisor not initialized. Open its conversation and continue it."
	} else {
		sup := a.chatSup
		se := doc.Session
		turnID := fmt.Sprintf("mission-%d-%d", se.ID, se.Rev)
		launch := func() (string, error) {
			h, already, err := sup.admit(se.ConversationID, turnID)
			if err != nil || already {
				return "", err
			}
			h.profileID = doc.Profile.ID // immutable even if the UI changes profile
			reply, err := sup.startTurn(h, fmt.Sprintf("Continue the approved mission session #%d. Read mission_status, then execute the ready work and verify the results.", se.ID), false, false, nil)
			if err != nil {
				sup.release(se.ConversationID, turnID)
			}
			return reply, err
		}
		queued := sup.afterCurrent(se.ConversationID, func() {
			if _, err := launch(); err != nil {
				log.Printf("task session %d: continuing its lead failed: %v", se.ID, err)
				// The UI was told "queued"; a failure now must reach the chat too.
				sup.reportContinuationFailure(doc.Profile.ID, se.ConversationID, turnID, se.ID, err)
			}
		})
		if queued {
			result["continuation"] = map[string]any{"turnId": turnID, "status": "queued"}
		} else if reply, err := launch(); err != nil {
			result["error"] = "The session is approved, but its lead could not start: " + err.Error() + ". Open its conversation and continue it."
		} else if reply != "" {
			var admission map[string]any
			if json.Unmarshal([]byte(reply), &admission) == nil {
				result["continuation"] = admission
			}
		}
	}
	out, _ := json.Marshal(result)
	return string(out)
}

// reportContinuationFailure tells the conversation that a queued lead start
// did not happen: a live-only error notice on the chat event channel (not
// journaled, seq MaxSafeSeq so the frontend never drops it as stale).
func (sup *chatSupervisor) reportContinuationFailure(profileID, conversationID, turnID string, sessionID int64, cause error) {
	if sup.emit == nil {
		return
	}
	ev, err := chatevents.New(profileID, conversationID, turnID, chatevents.MaxSafeSeq, time.Now(), chatevents.EventNotice, chatevents.NoticePayload{
		Code:     "mission.continue_failed",
		Message:  fmt.Sprintf("Mission session #%d is approved, but its lead could not start: %v. Open this conversation and continue it.", sessionID, cause),
		Severity: chatevents.SeverityError,
	})
	if err == nil {
		sup.emit(ev)
	}
}
