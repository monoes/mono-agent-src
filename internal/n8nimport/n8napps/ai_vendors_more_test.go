package n8napps

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apipkg"
	"github.com/monoes/mono-agent/internal/n8nimport"
)

// The Gemini, Ollama, MiniMax, Moonshot and Alibaba Cloud vendor nodes
// import as their API packages with the n8n credential's connection; the
// Databricks AI Agent imports as agent.ask.
func TestMoreAIVendorsImport(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	apipkg.Reload()
	t.Cleanup(apipkg.Reload)
	wf, rep, err := n8nimport.Convert([]byte(`{"name":"t","nodes":[
	 {"name":"M","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"name":"Gem","type":"@n8n/n8n-nodes-langchain.googleGemini","typeVersion":1.2,"position":[200,0],
	  "parameters":{"resource":"video","operation":"generate","modelId":{"__rl":true,"mode":"list","value":"models/veo-3.0-generate-001"},"prompt":"waves"},
	  "credentials":{"googlePalmApi":{"id":"1","name":"Gemini"}}},
	 {"name":"Chat","type":"@n8n/n8n-nodes-langchain.googleGemini","typeVersion":1.2,"position":[400,0],
	  "parameters":{"messages":{"values":[{"content":"hi"}]}},"credentials":{"googlePalmApi":{"id":"1","name":"Gemini"}}},
	 {"name":"Calc","type":"@n8n/n8n-nodes-langchain.toolCalculator","typeVersion":1,"position":[400,200],"parameters":{}},
	 {"name":"Llama","type":"@n8n/n8n-nodes-langchain.ollama","typeVersion":1,"position":[600,0],
	  "parameters":{"modelId":{"__rl":true,"mode":"list","value":"llama3.2"},"messages":{"values":[{"content":"hi"}]}},
	  "credentials":{"ollamaApi":{"id":"2","name":"Ollama"}}},
	 {"name":"Mini","type":"@n8n/n8n-nodes-langchain.minimax","typeVersion":1,"position":[800,0],
	  "parameters":{"messages":{"values":[{"content":"hi"}]}},"credentials":{"minimaxApi":{"id":"3","name":"MiniMax"}}},
	 {"name":"Kimi","type":"@n8n/n8n-nodes-langchain.moonshot","typeVersion":1,"position":[1000,0],
	  "parameters":{"resource":"image","modelId":"kimi-k2.5"},"credentials":{"moonshotApi":{"id":"4","name":"Kimi"}}},
	 {"name":"Qwen","type":"@n8n/n8n-nodes-langchain.alibabaCloud","typeVersion":1.1,"position":[1200,0],
	  "parameters":{"resource":"video","operation":"imageToVideo","prompt":"move"},"credentials":{"alibabaCloudApi":{"id":"5","name":"Qwen"}}},
	 {"name":"Agent","type":"@n8n/n8n-nodes-langchain.databricksAiAgent","typeVersion":2.2,"position":[1400,0],
	  "parameters":{"promptType":"define","text":"hello","exportTraces":true}}],
	 "connections":{"M":{"main":[[{"node":"Gem","type":"main","index":0}]]},
	  "Gem":{"main":[[{"node":"Chat","type":"main","index":0}]]},
	  "Calc":{"ai_tool":[[{"node":"Chat","type":"ai_tool","index":0}]]},
	  "Chat":{"main":[[{"node":"Llama","type":"main","index":0}]]},
	  "Llama":{"main":[[{"node":"Mini","type":"main","index":0}]]},
	  "Mini":{"main":[[{"node":"Kimi","type":"main","index":0}]]},
	  "Kimi":{"main":[[{"node":"Qwen","type":"main","index":0}]]},
	  "Qwen":{"main":[[{"node":"Agent","type":"main","index":0}]]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	cfgs := map[string]map[string]any{}
	types := map[string]string{}
	for _, n := range wf.Nodes {
		cfgs[n.Name], types[n.Name] = n.Config, n.Type
	}
	want := map[string][3]string{ // node → type, operation, credential_id
		"Gem":  {"app.gemini_api", "generate", "gemini_api"},
		"Kimi": {"app.moonshot_api", "analyze", "moonshot"},
		"Qwen": {"app.alibaba_cloud_api", "imageToVideo", "alibaba_cloud"},
	}
	for name, w := range want {
		c := cfgs[name]
		if types[name] != w[0] || c["operation"] != w[1] || c["credential_id"] != w[2] {
			t.Errorf("%s: %s %v, want %v", name, types[name], c, w)
		}
	}
	// Text messages run on agent.ask with the vendor connection's key.
	asks := map[string][2]string{ // node → credential_id, model
		"Chat (ask)":  {"gemini_api", "gemini-3-flash-preview"},
		"Llama (ask)": {"ollama", "llama3.2"},
		"Mini (ask)":  {"minimax", "MiniMax-M2.7"},
	}
	for name, w := range asks {
		c := cfgs[name]
		if types[name] != "agent.ask" || c["credential_id"] != w[0] || c["model"] != w[1] || c["prompt"] != "hi" {
			t.Errorf("%s: %s %v, want %v", name, types[name], c, w)
		}
	}
	agent := ""
	for name, typ := range types {
		if strings.HasPrefix(name, "Agent") && typ == "agent.ask" {
			agent = name
		}
	}
	if agent == "" || cfgs[agent]["prompt"] != "hello" {
		t.Errorf("databricks agent: %v %v", types, cfgs[agent])
	}
	issues := map[string]string{}
	for _, nr := range rep.Nodes {
		if nr.Status == n8nimport.StatusUnmapped {
			t.Errorf("%s unmapped: %v", nr.Name, nr.Issues)
		}
		for _, is := range nr.Issues {
			issues[nr.Name] += is.Message + "; "
		}
	}
	if !strings.Contains(issues["Chat"], `"Calc"`) {
		t.Errorf("Gemini chat does not report its tool: %q", issues["Chat"])
	}
	if !strings.Contains(issues["Agent"], "MLflow") {
		t.Errorf("Databricks agent does not report the trace export: %q", issues["Agent"])
	}
}
