package n8napps

import (
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/n8nimport"
)

// The OpenAI and Anthropic vendor nodes keep their text operations on the
// local agent (agent.ask); every other operation imports as the vendor's
// API package with the user's own key.
func TestAIVendorOperationsRouteToPackages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	apipkg.Reload()
	t.Cleanup(apipkg.Reload)
	wf, rep, err := n8nimport.Convert([]byte(`{"name":"t","nodes":[
	 {"name":"M","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"name":"Img","type":"@n8n/n8n-nodes-langchain.openAi","typeVersion":2.1,"position":[200,0],
	  "parameters":{"resource":"image","prompt":"a cat","options":{"returnImageUrls":true}},
	  "credentials":{"openAiApi":{"id":"1","name":"OpenAI"}}},
	 {"name":"Ask","type":"@n8n/n8n-nodes-langchain.openAi","typeVersion":2.3,"position":[400,0],
	  "parameters":{"responses":{"values":[{"content":"hi"}]}},"credentials":{"openAiApi":{"id":"1","name":"OpenAI"}}},
	 {"name":"Up","type":"@n8n/n8n-nodes-langchain.anthropic","typeVersion":1,"position":[600,0],
	  "parameters":{"resource":"file","inputType":"binary"},"credentials":{"anthropicApi":{"id":"2","name":"Claude"}}},
	 {"name":"Msg","type":"@n8n/n8n-nodes-langchain.anthropic","typeVersion":1,"position":[800,0],
	  "parameters":{"messages":{"values":[{"content":"hello"}]}}}],
	 "connections":{"M":{"main":[[{"node":"Img","type":"main","index":0}]]},
	  "Img":{"main":[[{"node":"Ask","type":"main","index":0}]]},
	  "Ask":{"main":[[{"node":"Up","type":"main","index":0}]]},
	  "Up":{"main":[[{"node":"Msg","type":"main","index":0}]]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	types := map[string]string{}
	for _, n := range wf.Nodes {
		byName[n.Name], types[n.Name] = n.Config, n.Type
	}
	img := byName["Img"]
	if types["Img"] != "app.openai_api" || img["resource"] != "image" || img["operation"] != "generate" ||
		img["credential_id"] != "openai" || img["legacyImageModel"] != true || img["prompt"] != "a cat" {
		t.Errorf("image node %s %v", types["Img"], img)
	}
	if types["Ask (ask)"] != "agent.ask" && types["Ask"] != "agent.ask" {
		t.Errorf("text response is not agent.ask: %v", types)
	}
	up := byName["Up"]
	if types["Up"] != "app.anthropic_api" || up["operation"] != "upload" || up["credential_id"] != "anthropic" || up["inputType"] != "binary" {
		t.Errorf("anthropic upload %s %v", types["Up"], up)
	}
	if types["Msg"] != "agent.ask" && types["Msg (ask)"] != "agent.ask" {
		t.Errorf("anthropic message is not agent.ask: %v", types)
	}
	for _, nr := range rep.Nodes {
		if nr.Status == n8nimport.StatusUnmapped {
			t.Errorf("%s unmapped: %v", nr.Name, nr.Issues)
		}
	}
}
