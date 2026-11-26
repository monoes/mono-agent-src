package crawl

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

type looseGenerator struct{ status string }

func (g looseGenerator) GenerateConfig(ctx context.Context, _, _, _ string, _ map[string]interface{}) (map[string]interface{}, error) {
	monomind.NoteSandbox(ctx, g.status) // as Exec does
	return map[string]interface{}{"fields": map[string]interface{}{"title": map[string]interface{}{"xpath": "h1.title"}}}, nil
}

// An item extracted with selectors a turn without a sandbox wrote says so.
func TestExtractPageSaysItsTurnRanWithoutASandbox(t *testing.T) {
	pageURL := servePage(t, productHTML)
	cfg := map[string]interface{}{"url": pageURL, "prompt": "the title"}
	got := runExtract(t, &ExtractPageNode{Generator: looseGenerator{monomind.SandboxStatusOff}}, cfg)
	if got["_agent_sandbox"] != monomind.SandboxStatusOff {
		t.Errorf("unsandboxed: %v", got)
	}
	got = runExtract(t, &ExtractPageNode{Generator: looseGenerator{monomind.SandboxStatusSandboxed}}, cfg)
	if v, ok := got["_agent_sandbox"]; ok {
		t.Errorf("sandboxed item says %v", v)
	}
}
