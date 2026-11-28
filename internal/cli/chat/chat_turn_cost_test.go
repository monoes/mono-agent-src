package chat

import (
	"database/sql"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderspend"
	"github.com/monoes/mono-agent/internal/monomind"
)

// A monomind with agent-exec-turn-cost reports what a resumed claude run cost by itself, so nothing is subtracted from
// it, whether it is the lead's turn or a resumed worker's follow-up, even when its cost and counters are all above the
// previous run's (#1744).
func TestAPerTurnCostMonomindIsChargedWhatItReports(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	bin, _ := writeOrgMonomind(t)
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(script), `"org-json-v1"]`, `"org-json-v1","agent-exec-turn-cost"]`, 1)
	patched = strings.Replace(patched, `"text":"The team found it."}'
  echo '{"v":1,"type":"done"`, `"text":"The team found it.","cost_usd":'"${ORG_LEAD_COST:-0}"',"model_usage":{"opus":{"input":'"${ORG_WORKER_TOKENS:-1}"',"output":'"${ORG_WORKER_TOKENS:-1}"',"cache_read":0,"cache_creation":0}}}'
  echo '{"v":1,"type":"done"`, 1)
	if !strings.Contains(patched, "agent-exec-turn-cost") || !strings.Contains(patched, "ORG_LEAD_COST") {
		t.Fatal("the fake monomind changed: patch it again")
	}
	if err := os.WriteFile(bin, []byte(patched), 0o755); err != nil {
		t.Fatal(err)
	}
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
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
	check := func(turn, lead, worker, tokens string, want float64) {
		t.Helper()
		t.Setenv("ORG_LEAD_COST", lead)
		t.Setenv("ORG_WORKER_COST", worker)
		t.Setenv("ORG_WORKER_TOKENS", tokens)
		if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", turn, "--", "where is the cache?"); err != nil {
			t.Fatalf("%s: %v\n%s", turn, err, out)
		}
		if got, err := coderspend.Spent(db, "default", conv.ID); err != nil || math.Abs(got-want) > 1e-9 {
			t.Fatalf("after %s the conversation spent %v (%v), want %v", turn, got, err, want)
		}
	}
	check("turn-1", "0.1", "0.4", "400", 0.5)
	t.Setenv("ORG_LEAD_CALL", `{"v":1,"type":"tool_call","id":"c1","name":"org_message","args":{"agent_id":"w1","text":"and eviction?"}}`)
	t.Setenv("ORG_LEAD_CALL2", `{"v":1,"type":"tool_call","id":"c2","name":"org_wait","args":{"agent_ids":["w1"],"timeout_s":30}}`)
	check("turn-2", "0.15", "0.45", "450", 1.1)
	check("turn-3", "0.2", "0.5", "500", 1.8)
}

// With such a monomind the compaction's cost, and the next turn's, are added whole to the turn's total, the mission's
// meter and the conversation's spend.
func TestAPerTurnCostCompactionIsAddedWhole(t *testing.T) {
	rig := newLeadSpendRig(t, "0.41 warn\n0.5\n0.6\n", true)
	bin := os.Getenv(monomind.EnvOverride)
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(script), `"capabilities":["agent-exec"`, `"capabilities":["agent-exec-turn-cost","agent-exec"`, 1)
	if patched == string(script) {
		t.Fatal("the fake monomind changed: patch it again")
	}
	if err := os.WriteFile(bin, []byte(patched), 0o755); err != nil {
		t.Fatal(err)
	}
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
	meter, conv := rig.turn("t1")
	wantSpend(t, "the compacted turn", meter, conv, 0.91)
	if got := rig.journaled("t1"); math.Abs(got-0.91) > 1e-9 {
		t.Errorf("the compacted turn's journaled total = %v, want 0.91", got)
	}
	meter, conv = rig.turn("t2")
	wantSpend(t, "the turn after it", meter, conv, 1.51)
}

// monomind is upgraded while a resumed turn runs, after every handshake that turn started on: its figure is its own
// cost, and nothing of the earlier turn is taken from it (#1744).
func TestAnUpgradeDuringAResumedTurnChargesItWhole(t *testing.T) {
	rig := newLeadSpendRig(t, "0.41\n0.5\n0.6\n", true)
	bin := os.Getenv(monomind.EnvOverride)
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	mark := bin + ".upgraded"
	patched := strings.Replace(string(script), `echo '{"v":1,"version":"9.0.0"`, `new=""; [ -f '`+mark+`' ] && new='"agent-exec-turn-cost",'; echo '{"v":1,"version":"9.0.0"`, 1)
	patched = strings.Replace(patched, `"capabilities":["agent-exec"`, `"capabilities":['"$new"'"agent-exec"`, 1)
	// the second turn's exec installs the new monomind just before its result
	patched = strings.Replace(patched, `  echo '{"v":1,"type":"result"`, `  [ "$1" = 0.5 ] && : > '`+mark+`'
  echo '{"v":1,"type":"result"`, 1)
	if strings.Count(patched, mark) != 2 || !strings.Contains(patched, `'"$new"'`) {
		t.Fatal("the fake monomind changed: patch it again")
	}
	if err := os.WriteFile(bin, []byte(patched), 0o755); err != nil {
		t.Fatal(err)
	}
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
	for _, step := range []struct {
		turn string
		want float64
	}{{"t1", 0.41}, {"t2", 0.91}, {"t3", 1.51}} {
		meter, conv := rig.turn(step.turn)
		wantSpend(t, step.turn, meter, conv, step.want)
	}
}
