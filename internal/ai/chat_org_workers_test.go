package ai

import (
	"slices"
	"testing"
)

func TestOrgWorkersRoundTrip(t *testing.T) {
	db := openTestDB(t)
	s, err := NewAIStore(db)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateConversationMode("default", "agent", "general", "claude", "", "", ModeCoder, "/w")
	if err != nil {
		t.Fatal(err)
	}
	w1 := OrgWorker{AgentID: "w1", TurnID: "t1", Role: "Researcher", AgentType: "researcher", Category: "research", Access: "research",
		Skills: []string{"search"}, Runtime: "claude", Model: "opus", Effort: "high", SessionID: "sess-1", Cwd: "/w",
		Report: "found it", Outcome: "done", AllowSpawn: true}
	for _, w := range []OrgWorker{w1, {AgentID: "w2", TurnID: "t1", Runtime: "codex"}, {AgentID: "w3", ParentID: "w1", TurnID: "t1", Runtime: "claude"}} {
		if err := s.SaveOrgWorker("default", conv.ID, w); err != nil {
			t.Fatal(err)
		}
	}
	// A later run of w1 replaces its row and makes it the latest.
	w1.TurnID, w1.SessionID, w1.Report = "t2", "sess-2", "and the eviction"
	if err := s.SaveOrgWorker("default", conv.ID, w1); err != nil {
		t.Fatal(err)
	}

	// A run that ended with no report (or no session) keeps the stored one.
	if err := s.SaveOrgWorker("default", conv.ID, OrgWorker{AgentID: "w2", TurnID: "t2", Runtime: "codex", SessionID: "sess-w2", Report: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOrgWorker("default", conv.ID, OrgWorker{AgentID: "w2", TurnID: "t3", Runtime: "codex", Outcome: "failed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOrgWorker("default", conv.ID, w1); err != nil { // w1 latest again
		t.Fatal(err)
	}
	all, err := s.ListOrgWorkers("default", conv.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, w := range all {
		ids = append(ids, w.AgentID)
	}
	if !slices.Equal(ids, []string{"w3", "w2", "w1"}) {
		t.Fatalf("workers oldest first = %v", ids)
	}
	got := all[2]
	got.UpdatedAt = ""
	if !slices.Equal(got.Skills, w1.Skills) {
		t.Errorf("skills = %v", got.Skills)
	}
	got.Skills = w1.Skills
	if got.AgentID != w1.AgentID || got.TurnID != "t2" || got.SessionID != "sess-2" || got.Report != "and the eviction" ||
		got.Cwd != "/w" || got.Model != "opus" || got.Role != "Researcher" || got.Access != "research" || !got.AllowSpawn ||
		got.Effort != "high" || got.Category != "research" || got.AgentType != "researcher" || got.Outcome != "done" {
		t.Errorf("w1 read back = %+v", got)
	}
	if w2 := all[1]; w2.AgentID != "w2" || w2.Report != "first" || w2.SessionID != "sess-w2" || w2.TurnID != "t3" || w2.Outcome != "failed" {
		t.Errorf("w2 after an empty run = %+v", w2)
	}
	if all[0].ParentID != "w1" {
		t.Errorf("w3 parent = %q", all[0].ParentID)
	}
	if latest, _ := s.ListOrgWorkers("default", conv.ID, 2); len(latest) != 2 || latest[1].AgentID != "w1" {
		t.Errorf("latest 2 = %+v", latest)
	}
	if other, _ := s.ListOrgWorkers("someone-else", conv.ID, 0); len(other) != 0 {
		t.Errorf("another profile sees %d workers", len(other))
	}

	// They go with their conversation.
	if err := s.DeleteConversation(conv.ID, "default"); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.ListOrgWorkers("default", conv.ID, 0); len(left) != 0 {
		t.Errorf("%d workers outlived their conversation", len(left))
	}
}

// The purpose record of a kept worker comes back as saved, and a row written
// before migration 145 reads back with neither (an unmapped veteran).
func TestOrgWorkersKeepTheirPurposeRecord(t *testing.T) {
	db := openTestDB(t)
	s, err := NewAIStore(db)
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateConversationMode("default", "agent", "general", "claude", "", "", ModeCoder, "/w")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveOrgWorker("default", conv.ID, OrgWorker{AgentID: "w1", TurnID: "t1", Runtime: "claude", Builtin: true, Purposes: "debugging,coding"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ai_chat_org_workers (profile_id, conversation_id, agent_id, turn_id, role, access, runtime, updated_at)
		VALUES ('default', ?, 'w2', 't0', 'Old', 'coding', 'claude', '2020-01-01T00:00:00.000000000Z')`, conv.ID); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListOrgWorkers("default", conv.ID, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %+v, %v", all, err)
	}
	by := map[string]OrgWorker{all[0].AgentID: all[0], all[1].AgentID: all[1]}
	if w := by["w1"]; !w.Builtin || w.Purposes != "debugging,coding" {
		t.Errorf("saved record read back as %+v", w)
	}
	if w := by["w2"]; w.Builtin || w.Purposes != "" {
		t.Errorf("a pre-145 row read back as %+v", w)
	}
}
