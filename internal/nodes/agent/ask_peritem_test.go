package agent

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The engine hands agent.ask its prompts unresolved (PerItemConfigFields);
// each item's prompt and system prompt are resolved against that item. Before,
// the engine resolved "{{ $json.w }}" once with the first item, so every item
// was asked about the first one.
func TestAskResolvesPromptPerItem(t *testing.T) {
	var _ workflow.PerItemConfigResolver = &AskNode{}
	var mu sync.Mutex
	var prompts, systems []string
	fakeTurns(t, func(o monomind.ExecOptions) (*monomind.TurnResult, error) {
		mu.Lock()
		prompts = append(prompts, o.Prompt)
		systems = append(systems, o.SystemPrompt)
		mu.Unlock()
		return &monomind.TurnResult{ResultText: "ok"}, nil
	})
	in := []workflow.Item{
		{JSON: map[string]interface{}{"w": "apple"}},
		{JSON: map[string]interface{}{"w": "carrot"}},
		{JSON: map[string]interface{}{"w": "salmon"}},
	}
	_, err := (&AskNode{}).Execute(context.Background(), workflow.NodeInput{Items: in}, map[string]interface{}{
		"runtime": "claude", "prompt": "Classify {{ $json.w }}.", "system_prompt": "Topic: {{$json.w}}", "concurrency": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(prompts)
	sort.Strings(systems)
	want := []string{"Classify apple.", "Classify carrot.", "Classify salmon."}
	for i, w := range want {
		if i >= len(prompts) || prompts[i] != w {
			t.Fatalf("prompts = %q, want %q", prompts, want)
		}
		if !strings.HasSuffix(systems[i], strings.TrimSuffix(strings.TrimPrefix(w, "Classify "), ".")) {
			t.Fatalf("system prompts = %q", systems)
		}
	}
}
