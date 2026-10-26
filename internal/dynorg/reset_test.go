package dynorg

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

func TestClearWorkerDropsSessionAndReport(t *testing.T) {
	kept := &keptWorkers{}
	ex := &treeExec{}
	c, _ := newVeteranConductor(t, ex, "/w", treeLimits, kept)
	ctx := context.Background()
	if _, err := c.Spawn(ctx, SpawnRequest{Brief: "map the cache", Access: ProfileResearch, Wait: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Handle(ctx, ToolClearWorker, json.RawMessage(`{"agent_id":"w1"}`)); err != nil {
		t.Fatal(err)
	}
	if v := kept.byID["w1"]; v.Session != "" || v.Report != "" || v.Outcome != chatevents.AgentDone {
		t.Errorf("kept after clear: %+v", v)
	}
	if _, err := c.Message(ctx, "w1", "start over: list the files"); err != nil {
		t.Fatal(err)
	}
	c.Wait(ctx, []string{"w1"}, 0)
	o, _ := ex.callFor("start over: list the files")
	if o.Resume != "" || strings.Contains(o.Prompt, "earlier report") {
		t.Errorf("a cleared worker was resumed or given its report: resume %q, prompt %q", o.Resume, o.Prompt)
	}
	if _, err := c.Handle(ctx, ToolClearWorker, json.RawMessage(`{"agent_id":"w9"}`)); err == nil {
		t.Error("cleared a worker that does not exist")
	}
	assertClean(t, c)
}

func TestCompactWorkerKeepsOnlyTheSummary(t *testing.T) {
	kept := &keptWorkers{}
	ex := &treeExec{}
	c, _ := newVeteranConductor(t, ex, "/w", treeLimits, kept)
	ctx := context.Background()
	if _, err := c.Spawn(ctx, SpawnRequest{Brief: "map the cache", Access: ProfileResearch, Wait: true}); err != nil {
		t.Fatal(err)
	}
	out, err := c.Handle(ctx, ToolCompactWorker, json.RawMessage(`{"agent_id":"w1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"w1"`) {
		t.Errorf("result = %s", out)
	}
	// The summary was asked of the worker's own session.
	o, _ := ex.callFor(compactWorkerPrompt)
	if o.Resume != "sess-opus" {
		t.Errorf("summary asked with resume %q", o.Resume)
	}
	v := kept.byID["w1"]
	if v.Session != "" || !strings.Contains(v.Report, "Summarize your work") {
		t.Errorf("kept after compact: %+v", v)
	}
	if got := c.Roster().Workers[0].Status; got != chatevents.AgentDone {
		t.Errorf("status after compact = %q", got)
	}
	// The next follow-up re-briefs from the summary instead of resuming.
	if _, err := c.Message(ctx, "w1", "now the eviction"); err != nil {
		t.Fatal(err)
	}
	c.Wait(ctx, []string{"w1"}, 0)
	o, ok := ex.callFor("Your earlier report:\ndid Summarize your work")
	if !ok || o.Resume != "" || !strings.HasSuffix(o.Prompt, "now the eviction") {
		t.Errorf("follow-up after compact: resume %q, prompt %q", o.Resume, o.Prompt)
	}
	assertClean(t, c)
}

func TestCompactWorkerNeedsASession(t *testing.T) {
	c, _ := newVeteranConductor(t, &treeExec{}, "/w", treeLimits, &keptWorkers{})
	c.AddVeterans([]Veteran{{ID: "w1", Role: "Researcher", Access: ProfileResearch, Runtime: "claude", Model: "opus", Cwd: "/w", Report: "r"}})
	if _, err := c.CompactWorker("w1"); err == nil || !strings.Contains(err.Error(), "no session") {
		t.Errorf("compact without a session: %v", err)
	}
}

func TestResetRefusesAWorkerThatIsStillRunning(t *testing.T) {
	ex := &treeExec{hold: 5e9}
	c, _ := newVeteranConductor(t, ex, "/w", treeLimits, &keptWorkers{})
	ctx := context.Background()
	if _, err := c.Spawn(ctx, SpawnRequest{Brief: "slow job", Access: ProfileResearch}); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{ToolClearWorker, ToolCompactWorker} {
		_, err := c.Handle(ctx, tool, json.RawMessage(`{"agent_id":"w1"}`))
		if err == nil || !strings.Contains(err.Error(), "is still working; org_wait for it first") {
			t.Errorf("%s on a running worker: %v", tool, err)
		}
	}
	c.Stop("w1")
	c.Wait(ctx, []string{"w1"}, 0)
}
