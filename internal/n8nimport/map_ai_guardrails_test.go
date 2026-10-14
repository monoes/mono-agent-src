package n8nimport_test

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A v2 classify workflow as n8n exports it: a chat trigger, the
// Guardrails node with an OpenAI chat model, and a node on each output.
const guardrailsV2 = `{"name":"Guarded chat","nodes":[
 {"id":"t","name":"When chat message received","type":"@n8n/n8n-nodes-langchain.chatTrigger","typeVersion":1.3,"position":[0,0],"webhookId":"abc","parameters":{"options":{}}},
 {"id":"g","name":"Guardrails","type":"@n8n/n8n-nodes-langchain.guardrails","typeVersion":2,"position":[200,0],"parameters":{
   "text":"={{ $json.chatInput }}",
   "guardrails":{
     "keywords":"refund, lawsuit",
     "jailbreak":{"value":{"threshold":0.8}},
     "nsfw":{"value":{"threshold":0.6,"customizePrompt":true,"prompt":"Flag anything rude."}},
     "pii":{"value":{"type":"selected","entities":["EMAIL_ADDRESS","CREDIT_CARD"]}},
     "secretKeys":{"value":{"permissiveness":"strict"}},
     "topicalAlignment":{"value":{"threshold":0.7,"prompt":"BUSINESS SCOPE: cooking recipes. Flag other topics."}},
     "urls":{"value":{"allowedUrls":"example.com, docs.acme.io","allowedSchemes":["https","mailto"],"blockUserinfo":true,"allowSubdomains":false}},
     "custom":{"guardrail":[{"name":"Competitors","threshold":0.5,"prompt":"Flag mentions of ACME Corp."}]},
     "customRegex":{"regex":[{"name":"ORDER_ID","value":"/ORD-\\d{6}/i"}]}
   },
   "customizeSystemMessage":true,
   "systemMessage":"Only answer with JSON."}},
 {"id":"m","name":"OpenAI Chat Model","type":"@n8n/n8n-nodes-langchain.lmChatOpenAi","typeVersion":1.2,"position":[200,200],"parameters":{"model":{"__rl":true,"mode":"list","value":"gpt-4.1-mini"},"options":{}},"credentials":{"openAiApi":{"id":"1","name":"OpenAI"}}},
 {"id":"p","name":"Passed","type":"n8n-nodes-base.noOp","typeVersion":1,"position":[400,0],"parameters":{}},
 {"id":"f","name":"Blocked","type":"n8n-nodes-base.noOp","typeVersion":1,"position":[400,200],"parameters":{}}
],"connections":{
 "When chat message received":{"main":[[{"node":"Guardrails","type":"main","index":0}]]},
 "OpenAI Chat Model":{"ai_languageModel":[[{"node":"Guardrails","type":"ai_languageModel","index":0}]]},
 "Guardrails":{"main":[[{"node":"Passed","type":"main","index":0}],[{"node":"Blocked","type":"main","index":0}]]}
}}`

func edgeHandle(t *testing.T, wf *workflow.WorkflowFile, from, to string) string {
	t.Helper()
	src, dst := node(t, wf, from), node(t, wf, to)
	for _, e := range wf.Connections {
		if e.Source == src.ID && e.Target == dst.ID {
			return e.SourceHandle
		}
	}
	t.Fatalf("no edge %s -> %s", from, to)
	return ""
}

func TestGuardrailsV2Classify(t *testing.T) {
	wf, rep, err := n8nimport.Convert([]byte(guardrailsV2))
	if err != nil {
		t.Fatal(err)
	}
	n := node(t, wf, "Guardrails")
	nr := nodeReport(t, rep, "Guardrails")
	if n.Type != "ai.guardrails" || nr.Status != n8nimport.StatusFull {
		t.Fatalf("type %s status %s:\n%s", n.Type, nr.Status, issueText(rep, "Guardrails"))
	}
	wantConfig(t, n, map[string]interface{}{
		"operation": "classify", "keywords": "refund, lawsuit",
		"jailbreak": true, "jailbreak_threshold": 0.8,
		"nsfw": true, "nsfw_threshold": 0.6, "nsfw_prompt": "Flag anything rude.",
		"pii": true, "pii_entities": []interface{}{"EMAIL_ADDRESS", "CREDIT_CARD"},
		"secret_keys": true, "secret_keys_permissiveness": "strict",
		"topical_alignment": true, "topical_alignment_threshold": 0.7,
		"topical_alignment_prompt": "BUSINESS SCOPE: cooking recipes. Flag other topics.",
		"urls":                     true, "allowed_urls": "example.com, docs.acme.io", "allowed_schemes": []interface{}{"https", "mailto"},
		"block_userinfo": true, "allow_subdomains": false,
		"custom":         []interface{}{map[string]interface{}{"name": "Competitors", "prompt": "Flag mentions of ACME Corp.", "threshold": 0.5}},
		"custom_regex":   []interface{}{map[string]interface{}{"name": "ORDER_ID", "pattern": `/ORD-\d{6}/i`}},
		"system_message": "Only answer with JSON.",
		"runtime":        "codex", "model": "gpt-4.1-mini",
	})
	if text, _ := n.Config["text"].(string); !strings.Contains(text, "{{") || !strings.Contains(text, "chatInput") {
		t.Errorf("text %q", text)
	}
	if h := edgeHandle(t, wf, "Guardrails", "Passed"); h != "pass" {
		t.Errorf("Pass output -> %q", h)
	}
	if h := edgeHandle(t, wf, "Guardrails", "Blocked"); h != "fail" {
		t.Errorf("Fail output -> %q", h)
	}
}

// v1 sanitize has no chat model input; its URL default placeholder means
// "block every URL".
func TestGuardrailsV1Sanitize(t *testing.T) {
	src := `{"name":"s","nodes":[{"id":"g","name":"Sanitize","type":"@n8n/n8n-nodes-langchain.guardrails","typeVersion":1,"position":[0,0],"parameters":{
	  "operation":"sanitize","text":"={{ $json.body }}",
	  "guardrails":{"pii":{"value":{"type":"all"}},"secretKeys":{"value":{"permissiveness":"balanced"}},
	    "urls":{"value":{"allowedUrls":"PLACEHOLDER","allowedSchemes":["https"]}}}}},
	 {"id":"n","name":"Next","type":"n8n-nodes-base.noOp","typeVersion":1,"position":[200,0],"parameters":{}}],
	 "connections":{"Sanitize":{"main":[[{"node":"Next","type":"main","index":0}]]}}}`
	wf, rep, err := n8nimport.Convert([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	n := node(t, wf, "Sanitize")
	if nr := nodeReport(t, rep, "Sanitize"); n.Type != "ai.guardrails" || nr.Status != n8nimport.StatusFull {
		t.Fatalf("type %s status %s:\n%s", n.Type, nr.Status, issueText(rep, "Sanitize"))
	}
	wantConfig(t, n, map[string]interface{}{"operation": "sanitize", "pii": true, "secret_keys": true,
		"secret_keys_permissiveness": "balanced", "urls": true, "allowed_schemes": []interface{}{"https"}})
	for _, k := range []string{"pii_entities", "allowed_urls", "runtime"} {
		if _, ok := n.Config[k]; ok {
			t.Errorf("%s set: %v", k, n.Config[k])
		}
	}
	if h := edgeHandle(t, wf, "Sanitize", "Next"); h != "main" {
		t.Errorf("handle %q", h)
	}
}

// Model-judged checks with no chat model attached use the claude runtime
// (noted); n8n's untouched topical prompt is reported.
func TestGuardrailsNoModelAndPlaceholderScope(t *testing.T) {
	src := `{"name":"s","nodes":[{"id":"g","name":"G","type":"@n8n/n8n-nodes-langchain.guardrails","typeVersion":2,"position":[0,0],"parameters":{
	  "text":"={{ $json.q }}","guardrails":{"jailbreak":{"value":{"threshold":0.7}},
	  "topicalAlignment":{"value":{"threshold":0.7}}}}}],"connections":{}}`
	wf, rep, err := n8nimport.Convert([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	n := node(t, wf, "G")
	wantConfig(t, n, map[string]interface{}{"runtime": "claude", "jailbreak": true, "topical_alignment": true})
	text := issueText(rep, "G")
	if !strings.Contains(text, "no chat model is attached") || !strings.Contains(text, "topical_alignment_prompt") {
		t.Errorf("issues:\n%s", text)
	}
	if nr := nodeReport(t, rep, "G"); nr.Status != n8nimport.StatusPartial {
		t.Errorf("status %s", nr.Status)
	}
}
