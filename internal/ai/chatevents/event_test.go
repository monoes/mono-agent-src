package chatevents

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNew_BuildsEnvelopeWithMarshaledPayload(t *testing.T) {
	at := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	ev, err := New("p1", "conv1", "turn1", 7, at, EventToolCompleted, ToolCompletedPayload{
		CallID: "tc_1",
		OK:     boolPtr(true),
		Result: "ok",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if ev.Version != Version {
		t.Errorf("Version = %d, want %d", ev.Version, Version)
	}
	if ev.ProfileID != "p1" || ev.ConversationID != "conv1" || ev.TurnID != "turn1" {
		t.Errorf("identity fields = %+v, want p1/conv1/turn1", ev)
	}
	if ev.Seq != 7 {
		t.Errorf("Seq = %d, want 7", ev.Seq)
	}
	if ev.At != "2026-09-11T09:00:00.000Z" {
		t.Errorf("At = %q, want the millisecond-precision UTC form", ev.At)
	}
	if ev.Type != EventToolCompleted {
		t.Errorf("Type = %q, want %q", ev.Type, EventToolCompleted)
	}
	var got ToolCompletedPayload
	if err := json.Unmarshal(ev.Payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.CallID != "tc_1" || got.OK == nil || !*got.OK || got.Result != "ok" {
		t.Errorf("payload round-trip = %+v", got)
	}
}

// Regression guard for the plan's explicit requirement: "False, zero, null
// and empty text are valid outputs" — a nil OK/metric must serialize to
// JSON null and deserialize back to nil, never coerce to false/0.
func TestToolCompletedPayload_NilOKRoundTripsToNullNotFalse(t *testing.T) {
	b, err := json.Marshal(ToolCompletedPayload{CallID: "tc_1", OK: nil, Result: ""})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if v, ok := raw["ok"]; !ok || v != nil {
		t.Errorf(`"ok" = %#v, want explicit JSON null`, v)
	}
	if v, ok := raw["result"]; !ok || v != "" {
		t.Errorf(`"result" = %#v, want present empty string, not absent`, v)
	}

	var got ToolCompletedPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OK != nil {
		t.Errorf("OK = %v, want nil (absent), not false", *got.OK)
	}
}

// Regression guard for usage: a present-but-zero token count must survive
// as 0, not be indistinguishable from "not reported" (nil).
func TestUsageUpdatedPayload_DistinguishesZeroFromAbsent(t *testing.T) {
	zero := int64(0)
	reported, err := json.Marshal(UsageUpdatedPayload{InputTokens: &zero, Source: "assistant"})
	if err != nil {
		t.Fatalf("marshal reported: %v", err)
	}
	var gotReported UsageUpdatedPayload
	if err := json.Unmarshal(reported, &gotReported); err != nil {
		t.Fatalf("unmarshal reported: %v", err)
	}
	if gotReported.InputTokens == nil || *gotReported.InputTokens != 0 {
		t.Errorf("InputTokens = %v, want a present *0", gotReported.InputTokens)
	}
	if gotReported.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil (never reported)", *gotReported.CostUSD)
	}

	absent, err := json.Marshal(UsageUpdatedPayload{Source: "assistant"})
	if err != nil {
		t.Fatalf("marshal absent: %v", err)
	}
	var gotAbsent UsageUpdatedPayload
	if err := json.Unmarshal(absent, &gotAbsent); err != nil {
		t.Fatalf("unmarshal absent: %v", err)
	}
	if gotAbsent.InputTokens != nil {
		t.Errorf("InputTokens = %v, want nil when never set", gotAbsent.InputTokens)
	}
}

func TestNew_InvalidPayloadReturnsError(t *testing.T) {
	_, err := New("p1", "c1", "t1", 1, time.Now(), EventNotice, make(chan int))
	if err == nil {
		t.Fatal("New with an unmarshalable payload unexpectedly succeeded")
	}
}

func boolPtr(b bool) *bool { return &b }

// turn.started carries a coder turn's attachments; a record written before
// they existed still decodes, with none, and a turn without any writes no
// attachments key.
func TestTurnStartedAttachmentsRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	want := []TurnAttachment{{Name: "shot.png", Path: "/w/.monoagent/attachments/t1/shot.png", Mime: "image/png", Size: 2048}}
	ev, err := New("p1", "c1", "t1", 1, at, EventTurnStarted, TurnStartedPayload{Backend: "agent", Text: "look", Attachments: want})
	if err != nil {
		t.Fatal(err)
	}
	var got TurnStartedPayload
	if err := json.Unmarshal(ev.Payload, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Attachments) != 1 || got.Attachments[0] != want[0] {
		t.Errorf("attachments = %+v, want %+v", got.Attachments, want)
	}

	var old TurnStartedPayload
	if err := json.Unmarshal([]byte(`{"backend":"agent","runtime":"claude","text":"hi"}`), &old); err != nil || old.Text != "hi" || old.Attachments != nil {
		t.Errorf("old record = %+v, %v", old, err)
	}
	plain, _ := New("p1", "c1", "t2", 1, at, EventTurnStarted, TurnStartedPayload{Backend: "agent", Text: "hi"})
	if string(plain.Payload) != `{"backend":"agent","text":"hi"}` {
		t.Errorf("payload without attachments = %s", plain.Payload)
	}
}

func TestMissionEventsRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	ev, err := New("p", "c", "t", 1, at, EventMissionPlan, MissionPlanPayload{SessionID: 4, Title: "T", Goal: "G", TaskCount: 3, EstimatedBudgetUSD: 1.5, Round: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "mission.plan" || EventMissionPropose != "mission.propose" {
		t.Errorf("kinds %q %q", ev.Type, EventMissionPropose)
	}
	var p MissionPlanPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil || p.SessionID != 4 || p.TaskCount != 3 || p.EstimatedBudgetUSD != 1.5 {
		t.Errorf("payload %+v %v", p, err)
	}
	var raw map[string]any
	_ = json.Unmarshal(ev.Payload, &raw)
	for _, k := range []string{"sessionId", "title", "goal", "taskCount", "estimatedBudgetUsd", "round"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("plan card lacks %s", k)
		}
	}
}

func TestMissionReportEventShape(t *testing.T) {
	at := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	ev, err := New("p", "c", "t", 2, at, EventMissionReport, MissionReportPayload{SessionID: 4, Title: "T", State: "done", TaskCount: 3, Done: 2, Verified: 1, Unfinished: []int64{}, Artifacts: 5, SpentUSD: 1.25, BudgetUSD: 10, Report: "# r"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != "mission.report" {
		t.Errorf("kind %q", ev.Type)
	}
	var raw map[string]any
	_ = json.Unmarshal(ev.Payload, &raw)
	for _, k := range []string{"sessionId", "title", "state", "taskCount", "done", "verified", "unfinished", "artifacts", "spentUsd", "budgetUsd", "report"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("report card lacks %s", k)
		}
	}
}
