package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// A worker of turn 1 comes back in turn 2 as an idle veteran, and the
// lead's org_message resumes its session (#230).
func TestDynamicOrgVeteranResumesInTheNextTurn(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeOrgMonomind(t)
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	cwd := t.TempDir()
	store := openTestChatStore(t, dbPath)
	conv, err := store.CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if _, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "dynamic"); code != 0 {
		t.Fatal("set-org failed")
	}
	if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "where is the cache?"); err != nil {
		t.Fatalf("turn 1: %v\n%s", err, out)
	}
	kept, err := store.ListOrgWorkers("default", conv.ID, 0)
	if err != nil || len(kept) != 1 {
		t.Fatalf("kept workers = %+v, %v", kept, err)
	}
	if w := kept[0]; w.AgentID != "w1" || w.SessionID != "th_worker" || w.Cwd != cwd || w.Runtime != "claude" || w.Model != "opus" ||
		w.Role != "Researcher" || w.TurnID != "turn-1" || w.Report != "The cache is in cache.go." {
		t.Errorf("kept w1 = %+v", w)
	} else if !w.Builtin || w.Purposes != "research" {
		t.Errorf("the purpose record wasn't kept with w1: builtin %v, purposes %q", w.Builtin, w.Purposes)
	}

	t.Setenv("ORG_LEAD_CALL", `{"v":1,"type":"tool_call","id":"c1","name":"org_message","args":{"agent_id":"w1","text":"and where is it evicted?"}}`)
	t.Setenv("ORG_LEAD_CALL2", `{"v":1,"type":"tool_call","id":"c2","name":"org_wait","args":{"agent_ids":["w1"],"timeout_s":30}}`)
	os.WriteFile(argsLog, nil, 0o644)
	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-2", "--", "and eviction?")
	if err != nil {
		t.Fatalf("turn 2: %v\n%s", err, out)
	}
	logged, _ := os.ReadFile(argsLog)
	resumed := false
	for _, l := range strings.Split(string(logged), "\n") {
		if strings.HasPrefix(l, "agent exec") && strings.Contains(l, "--access read") && strings.Contains(l, "--resume th_worker") {
			resumed = true
		}
	}
	if !resumed {
		t.Errorf("the veteran's exec didn't resume its session:\n%s", logged)
	}
	reply2, _ := os.ReadFile(filepath.Join(filepath.Dir(argsLog), "reply2.json"))
	if !strings.Contains(string(reply2), `\"status\":\"done\"`) || !strings.Contains(string(reply2), `\"veteran\":true`) {
		t.Errorf("org_wait on the veteran = %s", reply2)
	}

	j := parseJournaledTurn(t, out)
	evs := j.byType(chatevents.EventAgentSpawned)
	if len(evs) != 1 {
		t.Fatalf("turn 2 agent.spawned = %d", len(evs))
	}
	var sp chatevents.AgentSpawnedPayload
	json.Unmarshal(evs[0].Payload, &sp)
	if sp.AgentID != "w1" || !sp.Veteran || sp.Role != "Researcher" {
		t.Errorf("veteran spawned = %+v", sp)
	}
	var statuses []string
	for _, e := range j.byType(chatevents.EventAgentStatus) {
		var p chatevents.AgentStatusPayload
		json.Unmarshal(e.Payload, &p)
		if p.AgentID == "w1" && p.From != p.To {
			statuses = append(statuses, p.To)
		}
	}
	if len(statuses) < 3 || statuses[0] != chatevents.AgentIdle || statuses[1] != chatevents.AgentQueued || statuses[len(statuses)-1] != chatevents.AgentDone {
		t.Errorf("w1 statuses in turn 2 = %v", statuses)
	}
	if kept, _ := store.ListOrgWorkers("default", conv.ID, 0); len(kept) != 1 || kept[0].TurnID != "turn-2" {
		t.Errorf("after turn 2 kept = %+v", kept)
	}
}
