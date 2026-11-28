package dynorg

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// A worker with several questions asks them in one call: one card, one answer (#1803).
func TestAskUserTakesSeveralQuestionsAsOneCard(t *testing.T) {
	withFastAsk(t, time.Minute)
	ans := &fakeAnswers{}
	var got string
	var refused error
	exec := func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
		out, err := o.OnToolCall(ctx, ToolAskUser, json.RawMessage(`{"questions":["Which database?","Which region?"]}`))
		if err != nil {
			return nil, err
		}
		got = out
		_, refused = o.OnToolCall(ctx, ToolAskUser, json.RawMessage(`{"questions":["c?","d?"]}`))
		return okTurn("done"), nil
	}
	em := &recEmitter{}
	c := New(context.Background(), Config{Cwd: "/w", Staffer: &Staffer{Roster: []Model{askModel}, Lead: askModel}, Exec: exec, Emit: em, Answers: ans,
		UI: &UIConfig{}, Limits: Limits{MaxAgents: 4, MaxConcurrent: 4}})
	t.Cleanup(c.Close)
	done := make(chan error, 1)
	go func() {
		_, err := c.Spawn(context.Background(), SpawnRequest{Brief: "build it", Wait: true})
		done <- err
	}()
	waitFor(t, func() bool { return len(systemSurfaces(em)) > 0 })
	c.ui.mu.Lock()
	card := c.ui.reg.Surface("ask-w1.q1")
	c.ui.mu.Unlock()
	if card == nil || card.Components["q-q0"]["text"] != "Which database?" || card.Components["q-q1"]["text"] != "Which region?" {
		t.Fatalf("card %+v", card)
	}
	var asked []string
	em.mu.Lock()
	for _, e := range em.events {
		if m, ok := e.payload.(chatevents.AgentMessagePayload); ok && m.Direction == "question" {
			asked = append(asked, m.Text)
		}
	}
	em.mu.Unlock()
	if len(asked) != 1 || asked[0] != "1. Which database?\n2. Which region?" {
		t.Fatalf("journaled %q", asked)
	}
	ans.put("w1", "q1", "both answers")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "both answers") {
		t.Fatalf("answer %q", got)
	}
	// The limit counts questions, not calls.
	if refused == nil || !strings.Contains(refused.Error(), "1 more") {
		t.Fatalf("refusal %v", refused)
	}
}

func TestAskRuleAsksForOneCall(t *testing.T) {
	if !strings.Contains(askRule, "one call") {
		t.Fatalf("rule %q", askRule)
	}
	if _, ok := askSpec().Schema["properties"].(map[string]any)["questions"]; !ok {
		t.Fatalf("schema %+v", askSpec().Schema)
	}
}
