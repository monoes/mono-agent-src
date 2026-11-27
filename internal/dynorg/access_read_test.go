package dynorg

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// researchRun spawns a writer and a researcher on opus side by side, with
// monomind's --access read capability present or not, and returns the
// researcher's exec, the most execs that ran at once, and its statuses.
func researchRun(t *testing.T, readAccess bool) (monomind.ExecOptions, int32, []chatevents.AgentStatusPayload) {
	t.Helper()
	ex := &execScript{hold: 60 * time.Millisecond}
	em := &recEmitter{}
	c := New(context.Background(), Config{
		Cwd: "/w", ReadAccess: readAccess, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		Exec: ex.exec, Emit: em, Limits: Limits{MaxAgents: 3, MaxConcurrent: 3},
	})
	if _, err := c.Spawn(context.Background(), SpawnRequest{Brief: "implement it", Access: ProfileCoding}); err != nil {
		t.Fatal(err)
	}
	r, err := c.Spawn(context.Background(), SpawnRequest{Brief: "investigate it", Access: ProfileResearch})
	if err != nil {
		t.Fatal(err)
	}
	c.Wait(context.Background(), nil, 5*time.Second)
	c.Close()
	var research monomind.ExecOptions
	for _, o := range ex.calls {
		if strings.Contains(o.Prompt, "investigate") {
			research = o
		}
	}
	var statuses []chatevents.AgentStatusPayload
	for _, p := range em.find(chatevents.EventAgentStatus) {
		if s := p.(chatevents.AgentStatusPayload); s.AgentID == r.ID {
			statuses = append(statuses, s)
		}
	}
	return research, ex.maxRun, statuses
}

func confinementOf(statuses []chatevents.AgentStatusPayload) string {
	for _, s := range statuses {
		if s.To == chatevents.AgentWorking {
			return s.Confinement
		}
	}
	return ""
}

func heldWrite(statuses []chatevents.AgentStatusPayload) bool {
	return slices.ContainsFunc(statuses, func(s chatevents.AgentStatusPayload) bool { return slices.Contains(s.Leases, "write") })
}

// With agent-exec-access-read a research worker runs on --access read: no
// full access, no write lease, beside the writer.
func TestResearchOnAccessReadHoldsNoWriteLease(t *testing.T) {
	o, maxRun, statuses := researchRun(t, true)
	if o.Access != monomind.AccessRead || o.Sandbox != "" {
		t.Errorf("research exec = access %q sandbox %q, want read", o.Access, o.Sandbox)
	}
	if maxRun != 2 {
		t.Errorf("the researcher must run beside the writer: max running = %d, want 2", maxRun)
	}
	if heldWrite(statuses) {
		t.Errorf("the researcher held the write lease: %+v", statuses)
	}
	if c := confinementOf(statuses); c != chatevents.ConfinementAccessRead {
		t.Errorf("journaled confinement = %q, want %q", c, chatevents.ConfinementAccessRead)
	}
}

// Without it (older monomind), the old fallback: full access, told to
// stay read-only, holding the write lease so it never overlaps a writer.
func TestResearchWithoutAccessReadFallsBackToTheWriteLease(t *testing.T) {
	o, maxRun, statuses := researchRun(t, false)
	if o.Access != monomind.AccessFull || o.Sandbox != "" {
		t.Errorf("research exec = access %q sandbox %q, want full", o.Access, o.Sandbox)
	}
	if !strings.Contains(o.SystemPrompt, "Do not edit") {
		t.Error("the fallback must still tell the worker to stay read-only")
	}
	if maxRun != 1 {
		t.Errorf("the unconfined researcher must wait for the writer: max running = %d, want 1", maxRun)
	}
	if !heldWrite(statuses) {
		t.Errorf("the unconfined researcher never held the write lease: %+v", statuses)
	}
	if c := confinementOf(statuses); c != chatevents.ConfinementWriteLease {
		t.Errorf("journaled confinement = %q, want %q", c, chatevents.ConfinementWriteLease)
	}
}

// The rule staffs research on a model that runs read-only before a cheaper
// one that would have to hold the write lease.
func TestRuleModelPrefersConfinedResearch(t *testing.T) {
	cheapBare := Model{Runtime: "grok", Model: "g", FullAccess: true, CostUSD: 0.0001}
	sandboxed := Model{Runtime: "codex", Model: "c", FullAccess: true, ReadOnlySandbox: true, CostUSD: 0.01}
	if got := ruleModel([]Model{cheapBare, opus, haiku}, ProfileResearch, false, false, func(Model) float64 { return 0.75 }); got.Key() != haiku.Key() {
		t.Errorf("research = %s, want the cheapest read-access model %s", got.Key(), haiku.Key())
	}
	if got := ruleModel([]Model{cheapBare, sandboxed}, ProfileResearch, false, false, func(Model) float64 { return 0.75 }); got.Key() != sandboxed.Key() {
		t.Errorf("research = %s, want the sandboxed model", got.Key())
	}
	if got := ruleModel([]Model{cheapBare, {Runtime: "x", FullAccess: true, CostUSD: 1}}, ProfileResearch, false, false, func(Model) float64 { return 0.75 }); got.Key() != cheapBare.Key() {
		t.Errorf("with nothing confined, research = %s, want the cheapest", got.Key())
	}
}
