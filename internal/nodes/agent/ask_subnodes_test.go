package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/workflow"
)

func modelSub(runtime, model string) workflow.SubNode {
	return workflow.SubNode{ID: "m", Name: "Chat Model", Type: "ai.model", Kind: "ai_languageModel",
		Config: map[string]interface{}{"runtime": runtime, "model": model}}
}

func TestAskModelSubNodeSetsRuntimeAndModel(t *testing.T) {
	var got []monomind.ExecOptions
	fakeTurns(t, func(o monomind.ExecOptions) (*monomind.TurnResult, error) {
		got = append(got, o)
		return &monomind.TurnResult{ResultText: "ok"}, nil
	})
	in := workflow.NodeInput{Items: items(1), SubNodes: []workflow.SubNode{modelSub("opencode", "sub-model"), modelSub("codex", "second")}}
	// No runtime of its own: the sub-node supplies it; it wins over an inline one.
	for _, cfg := range []map[string]interface{}{
		{"prompt": "hi"},
		{"prompt": "hi", "runtime": "claude", "model": "inline"},
	} {
		if _, err := (&AskNode{}).Execute(context.Background(), in, cfg); err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range got {
		if o.Runtime != "opencode" || o.Model != "sub-model" {
			t.Fatalf("turn ran %s/%s", o.Runtime, o.Model)
		}
	}
	// A sub-node without a runtime is a config error naming it.
	in.SubNodes = []workflow.SubNode{modelSub("", "")}
	_, err := (&AskNode{}).Execute(context.Background(), in, map[string]interface{}{"prompt": "hi", "runtime": "claude"})
	if !errors.Is(err, workflow.ErrInvalidConfig) || !strings.Contains(err.Error(), "Chat Model") {
		t.Fatalf("err = %v", err)
	}
}

func TestAskMemorySubNodeKeepsConversation(t *testing.T) {
	ctx, db := memCtx(t)
	var prompts []string
	fakeTurns(t, func(o monomind.ExecOptions) (*monomind.TurnResult, error) {
		prompts = append(prompts, o.Prompt)
		return &monomind.TurnResult{ResultText: "noted"}, nil
	})
	mem := workflow.SubNode{ID: "mem", Name: "Simple Memory", Type: "ai.memory", Kind: "ai_memory",
		Config: map[string]interface{}{"session_key": "{{ $json.sessionId }}", "window": float64(10)}}
	ask := func(text string) {
		in := workflow.NodeInput{Items: []workflow.Item{{JSON: map[string]interface{}{"sessionId": "s9", "chatInput": text}}},
			SubNodes: []workflow.SubNode{modelSub("claude", ""), mem}}
		if _, err := (&AskNode{}).Execute(ctx, in, map[string]interface{}{"prompt": "{{ $json.chatInput }}"}); err != nil {
			t.Fatal(err)
		}
	}
	ask("my name is Ada")
	ask("what is my name?")
	if !strings.Contains(prompts[1], "User: my name is Ada") {
		t.Fatalf("second prompt has no history: %q", prompts[1])
	}
	want := []string{"human:my name is Ada", "ai:noted", "human:what is my name?", "ai:noted"}
	if got := stored(t, db, "p1", "s9"); !reflect.DeepEqual(got, want) {
		t.Fatalf("session = %v", got)
	}
}

func TestAskOutputParserSubNodeEnforcesSchema(t *testing.T) {
	var prompts []string
	replies := []string{`{"label": "x"}`, `{"category": "lead", "score": 2}`}
	fakeTurns(t, func(o monomind.ExecOptions) (*monomind.TurnResult, error) {
		prompts = append(prompts, o.Prompt)
		r := replies[0]
		replies = replies[1:]
		return &monomind.TurnResult{ResultText: r}, nil
	})
	parser := workflow.SubNode{ID: "p", Name: "Parser", Type: "ai.output_parser", Kind: "ai_outputParser",
		Config: map[string]interface{}{"schema_type": "example", "json_example": `{"category": "lead", "score": 1}`}}
	in := workflow.NodeInput{Items: items(1), SubNodes: []workflow.SubNode{parser}}
	out, err := (&AskNode{}).Execute(context.Background(), in, map[string]interface{}{"runtime": "claude", "prompt": "classify"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[0], "JSON Schema") {
		t.Fatalf("prompts %q (want a re-ask after the mismatch)", prompts)
	}
	res, _ := out[0].Items[0].JSON["agent_response"].(map[string]interface{})
	if res["category"] != "lead" || res["score"] != float64(2) {
		t.Fatalf("answer %v", out[0].Items[0].JSON)
	}
	// A broken parser is a config error.
	in.SubNodes[0].Config = map[string]interface{}{"schema_type": "schema", "json_schema": "{not json"}
	if _, err := (&AskNode{}).Execute(context.Background(), in, map[string]interface{}{"runtime": "claude", "prompt": "classify"}); !errors.Is(err, workflow.ErrInvalidConfig) {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentMemorySubNodeNamesSession(t *testing.T) {
	ctx, db := memCtx(t)
	in := workflow.NodeInput{Items: []workflow.Item{{JSON: map[string]interface{}{"conv": "k1"}}},
		SubNodes: []workflow.SubNode{{ID: "mem", Type: "ai.memory", Kind: "ai_memory",
			Config: map[string]interface{}{"session_key": "{{ $json.conv }}"}}}}
	if _, err := (&MemoryNode{}).Execute(ctx, in, map[string]interface{}{"operation": "insert",
		"messages": []interface{}{map[string]interface{}{"role": "human", "content": "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, db, "p1", "k1"); !reflect.DeepEqual(got, []string{"human:hello"}) {
		t.Fatalf("session k1 = %v", got)
	}
}
