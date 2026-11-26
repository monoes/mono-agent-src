package chat

import (
	"context"
	"database/sql"
	"math"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/coderspend"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/monomind"
)

// A worker kept for the next turn resumes its claude session, whose reported cost is the session's total (#1744):
// the conversation's spend counts the worker once, through the stored worker and the journal of every turn.
func TestDynamicOrgResumedWorkerCostsTheConversationOnce(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	bin, _ := writeOrgMonomind(t)
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	store := openTestChatStore(t, dbPath)
	conv, err := store.CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "dynamic"); code != 0 {
		t.Fatal("set-org failed")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	check := func(turn, reported string, want float64) {
		t.Helper()
		t.Setenv("ORG_WORKER_COST", reported)
		if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", turn, "--", "where is the cache?"); err != nil {
			t.Fatalf("%s: %v\n%s", turn, err, out)
		}
		if got, err := coderspend.Spent(db, "default", conv.ID); err != nil || math.Abs(got-want) > 1e-9 {
			t.Fatalf("after %s the conversation spent %v (%v), want %v", turn, got, err, want)
		}
	}
	check("turn-1", "0.4", 0.4)
	t.Setenv("ORG_LEAD_CALL", `{"v":1,"type":"tool_call","id":"c1","name":"org_message","args":{"agent_id":"w1","text":"and eviction?"}}`)
	t.Setenv("ORG_LEAD_CALL2", `{"v":1,"type":"tool_call","id":"c2","name":"org_wait","args":{"agent_ids":["w1"],"timeout_s":30}}`)
	check("turn-2", "0.7", 0.7)
	check("turn-3", "0.9", 0.9)
}

type discardEvents struct{}

func (discardEvents) Emit(chatevents.EventType, any) {}

// An AI task's workers share a pool: a resumed claude run takes from it what it added, so the pool bounds what the
// workers really spend.
func TestTaskWorkersPoolCountsAResumedClaudeRunOnce(t *testing.T) {
	r, st, cfg := workersRig(t)
	opts := monomind.ExecOptions{BudgetUSD: 1} // the pool is 0.50
	totals := []float64{0.2, 0.3, 0.55}
	var caps []float64
	inner := func(_ context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
		total := totals[len(caps)]
		caps = append(caps, o.BudgetUSD)
		on(monomind.Event{Type: monomind.EventSession, SessionID: "s1"})
		return &monomind.TurnResult{SawDone: true, ResultText: "ok", StopReason: monomind.StopEndTurn, SessionID: "s1", CostUSD: total, HasCostUSD: true}, nil
	}
	_, exec, _, _ := taskWorkers(st.DB, cfg, r.convID, "turn-1", coderSettings{}, &opts, inner)
	m := dynorg.Model{Runtime: "claude", Model: "opus", FullAccess: true, Resume: true, ReportsCost: true}
	c := dynorg.New(context.Background(), dynorg.Config{Cwd: t.TempDir(), Staffer: &dynorg.Staffer{Roster: []dynorg.Model{m}, Lead: m},
		Exec: exec, Emit: discardEvents{}})
	t.Cleanup(c.Close)
	if _, err := c.Spawn(context.Background(), dynorg.SpawnRequest{Brief: "implement", Runtime: "claude", Model: "opus", Wait: true}); err != nil {
		t.Fatal(err)
	}
	for range totals[1:] {
		if _, err := c.Message(context.Background(), "w1", "more"); err != nil {
			t.Fatal(err)
		}
		c.Wait(context.Background(), []string{"w1"}, 10*time.Second)
	}
	if len(caps) != 3 || math.Abs(caps[0]-0.5) > 1e-9 || math.Abs(caps[1]-0.3) > 1e-9 || math.Abs(caps[2]-0.2) > 1e-9 {
		t.Fatalf("caps = %v, want 0.5, then the 0.3 and 0.2 left", caps)
	}
	var used float64
	if err := st.DB.QueryRow(`SELECT COALESCE(SUM(spent_usd), 0) FROM ai_budget_pool`).Scan(&used); err != nil || math.Abs(used-0.55) > 1e-9 {
		t.Fatalf("the pool spent %v (%v), want 0.55", used, err)
	}
}
