package n8nimport_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/workflow"
)

// chatWithMemory is an n8n chat workflow: chat trigger → AI Agent, with a
// chat model and a memory sub-node attached to the agent.
func chatWithMemory(memType string, memVersion float64, memParams map[string]interface{}) string {
	p, _ := json.Marshal(memParams)
	return `{"name":"chat","nodes":[
	 {"id":"1","name":"When chat message received","type":"@n8n/n8n-nodes-langchain.chatTrigger","typeVersion":1.1,"position":[0,0],"parameters":{"options":{}},"webhookId":"w1"},
	 {"id":"2","name":"AI Agent","type":"@n8n/n8n-nodes-langchain.agent","typeVersion":1.7,"position":[200,0],"parameters":{"options":{}}},
	 {"id":"3","name":"OpenAI Chat Model","type":"@n8n/n8n-nodes-langchain.lmChatOpenAi","typeVersion":1.2,"position":[100,200],"parameters":{"model":{"__rl":true,"value":"gpt-4o-mini","mode":"list"},"options":{}}},
	 {"id":"4","name":"Memory","type":"` + memType + `","typeVersion":` + jsonNum(memVersion) + `,"position":[250,200],"parameters":` + string(p) + `}
	],"connections":{
	 "When chat message received":{"main":[[{"node":"AI Agent","type":"main","index":0}]]},
	 "OpenAI Chat Model":{"ai_languageModel":[[{"node":"AI Agent","type":"ai_languageModel","index":0}]]},
	 "Memory":{"ai_memory":[[{"node":"AI Agent","type":"ai_memory","index":0}]]}
	}}`
}

func jsonNum(f float64) string { b, _ := json.Marshal(f); return string(b) }

func convertRaw(t *testing.T, src string) (*workflow.WorkflowFile, *n8nimport.Report) {
	t.Helper()
	wf, rep, err := n8nimport.Convert([]byte(src))
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	return wf, rep
}

func validates(t *testing.T, wf *workflow.WorkflowFile) {
	t.Helper()
	raw, err := json.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := workflow.ParseWorkflowFileBytes(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rep := workflow.ValidateWorkflow(&parsed, workflow.ValidateOptions{KnownNodeType: noderegistry.Build(nil).Has})
	if !rep.Valid {
		t.Errorf("invalid: %v", rep.Errors)
	}
	for _, is := range rep.Issues {
		if is.Code == "unknown_field" || is.Code == "invalid_option" {
			t.Errorf("validation issue: %+v", is)
		}
	}
}

func TestAgentWithBufferWindowMemoryKeepsConversation(t *testing.T) {
	wf, rep := convertRaw(t, chatWithMemory("@n8n/n8n-nodes-langchain.memoryBufferWindow", 1.3,
		map[string]interface{}{"contextWindowLength": 8}))
	n := node(t, wf, "AI Agent")
	wantConfig(t, n, map[string]interface{}{"memory": map[string]interface{}{"session_key": "{{ $json.sessionId }}", "window": 16}})
	nr := nodeReport(t, rep, "AI Agent")
	text := issueText(rep, "AI Agent")
	if nr.Status != n8nimport.StatusFull || !strings.Contains(text, "local chat memory") || strings.Contains(text, "not kept") {
		t.Fatalf("status %s:\n%s", nr.Status, text)
	}
	validates(t, wf)
}

func TestAgentMemorySessionKeysAndBackends(t *testing.T) {
	cases := []struct {
		typ         string
		version     float64
		params      map[string]interface{}
		session     string
		window      int
		noteBackend string
	}{
		{"memoryBufferWindow", 1.4, map[string]interface{}{}, "{{ $json.sessionId }}", 10, ""},
		{"memoryBufferWindow", 1.4, map[string]interface{}{"sessionIdType": "customKey", "sessionKey": "={{ $json.user }}-chat"}, "{{ $json.user }}-chat", 10, ""},
		{"memoryBufferWindow", 1, map[string]interface{}{}, "chat_history", 10, ""},
		{"memoryBufferWindow", 1.1, map[string]interface{}{}, "{{ $json.sessionId }}", 10, ""},
		{"memoryRedisChat", 1.6, map[string]interface{}{"sessionIdType": "customKey", "sessionKey": "support", "contextWindowLength": 3}, "support", 6, "Redis"},
		{"memoryRedisChat", 1.2, map[string]interface{}{}, "{{ $json.sessionId }}", 0, "Redis"},
		{"memoryPostgresChat", 1.4, map[string]interface{}{"tableName": "hist"}, "{{ $json.sessionId }}", 10, "Postgres"},
		{"memoryMongoDbChat", 1.1, map[string]interface{}{"sessionIdType": "customKey", "sessionKey": "={{ $json.chatId }}"}, "{{ $json.chatId }}", 10, "MongoDB"},
		{"memoryXata", 1.1, map[string]interface{}{"sessionId": "={{ $json.conv }}"}, "{{ $json.conv }}", 0, "Xata"},
		{"memoryMotorhead", 1.4, map[string]interface{}{}, "{{ $json.sessionId }}", 10, "Motorhead"},
	}
	for _, c := range cases {
		wf, rep := convertRaw(t, chatWithMemory("@n8n/n8n-nodes-langchain."+c.typ, c.version, c.params))
		n := node(t, wf, "AI Agent")
		mem, _ := n.Config["memory"].(map[string]interface{})
		if mem == nil || mem["session_key"] != c.session || mem["window"] != c.window {
			t.Errorf("%s %v: memory = %#v", c.typ, c.version, n.Config["memory"])
		}
		text := issueText(rep, "AI Agent")
		if c.noteBackend != "" && !strings.Contains(text, "instead of "+c.noteBackend) {
			t.Errorf("%s: report lacks the backend note:\n%s", c.typ, text)
		}
		if nr := nodeReport(t, rep, "AI Agent"); nr.Status != n8nimport.StatusFull {
			t.Errorf("%s: status %s:\n%s", c.typ, nr.Status, text)
		}
	}
}

func TestAgentWithZepMemoryStillWarns(t *testing.T) {
	wf, rep := convertRaw(t, chatWithMemory("@n8n/n8n-nodes-langchain.memoryZep", 1.3, map[string]interface{}{}))
	if _, ok := node(t, wf, "AI Agent").Config["memory"]; ok {
		t.Error("Zep memory mapped")
	}
	if text := issueText(rep, "AI Agent"); !strings.Contains(text, "not kept between runs") {
		t.Errorf("report:\n%s", text)
	}
}

// memoryWorkflow is a Chat Memory Manager (or retriever) with a Redis memory
// keyed by a custom session.
func memoryWorkflow(typ string, version float64, params map[string]interface{}) string {
	p, _ := json.Marshal(params)
	return `{"name":"mem","nodes":[
	 {"id":"1","name":"Start","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
	 {"id":"2","name":"Mem","type":"@n8n/n8n-nodes-langchain.` + typ + `","typeVersion":` + jsonNum(version) + `,"position":[200,0],"parameters":` + string(p) + `},
	 {"id":"3","name":"Redis Chat Memory","type":"@n8n/n8n-nodes-langchain.memoryRedisChat","typeVersion":1.5,"position":[200,200],"parameters":{"sessionIdType":"customKey","sessionKey":"={{ $json.chatId }}"}}
	],"connections":{
	 "Start":{"main":[[{"node":"Mem","type":"main","index":0}]]},
	 "Redis Chat Memory":{"ai_memory":[[{"node":"Mem","type":"ai_memory","index":0}]]}
	}}`
}

func TestMemoryManagerOperations(t *testing.T) {
	cases := []struct {
		name    string
		version float64
		params  map[string]interface{}
		want    map[string]interface{}
	}{
		{"load default", 1.1, map[string]interface{}{},
			map[string]interface{}{"operation": "get", "format": "pairs", "group": true}},
		{"load raw ungrouped", 1.1, map[string]interface{}{"simplifyOutput": false, "options": map[string]interface{}{"groupMessages": false}},
			map[string]interface{}{"operation": "get", "format": "raw", "group": false}},
		{"insert", 1.1, map[string]interface{}{"mode": "insert", "messages": map[string]interface{}{"messageValues": []interface{}{
			map[string]interface{}{"type": "user", "message": "={{ $json.question }}"},
			map[string]interface{}{"type": "ai", "message": "noted", "hideFromUI": true},
			map[string]interface{}{"message": "be brief"},
		}}}, map[string]interface{}{"operation": "insert", "messages": []interface{}{
			map[string]interface{}{"role": "human", "content": "{{ $json.question }}"},
			map[string]interface{}{"role": "ai", "content": "noted"},
			map[string]interface{}{"role": "system", "content": "be brief"},
		}}},
		{"insert override", 1.1, map[string]interface{}{"mode": "insert", "insertMode": "override"},
			map[string]interface{}{"operation": "insert", "insert_mode": "override", "messages": []interface{}{}}},
		{"delete last n", 1.1, map[string]interface{}{"mode": "delete", "lastMessagesCount": 4},
			map[string]interface{}{"operation": "delete", "count": 4}},
		{"delete all", 1.1, map[string]interface{}{"mode": "delete", "deleteMode": "all"},
			map[string]interface{}{"operation": "delete", "delete_mode": "all"}},
	}
	for _, c := range cases {
		wf, rep := convertRaw(t, memoryWorkflow("memoryManager", c.version, c.params))
		n := node(t, wf, "Mem")
		if n.Type != "agent.memory" {
			t.Fatalf("%s: type %s:\n%s", c.name, n.Type, issueText(rep, "Mem"))
		}
		c.want["session_key"] = "{{ $json.chatId }}"
		wantConfig(t, n, c.want)
		text := issueText(rep, "Mem")
		if nr := nodeReport(t, rep, "Mem"); nr.Status != n8nimport.StatusFull || !strings.Contains(text, "instead of Redis") {
			t.Errorf("%s: status %s:\n%s", c.name, nr.Status, text)
		}
		validates(t, wf)
	}
	// Version 1 output the messages after insert/delete: reported.
	_, rep := convertRaw(t, memoryWorkflow("memoryManager", 1, map[string]interface{}{"mode": "delete", "deleteMode": "all"}))
	if nr := nodeReport(t, rep, "Mem"); nr.Status != n8nimport.StatusPartial {
		t.Errorf("v1 delete: status %s", nr.Status)
	}
}

func TestMemoryChatRetriever(t *testing.T) {
	wf, rep := convertRaw(t, memoryWorkflow("memoryChatRetriever", 1, map[string]interface{}{}))
	n := node(t, wf, "Mem")
	wantConfig(t, n, map[string]interface{}{"operation": "get", "format": "pairs", "group": false,
		"execute_once": true, "session_key": "{{ $json.chatId }}"})
	if nr := nodeReport(t, rep, "Mem"); nr.Status != n8nimport.StatusFull {
		t.Errorf("status %s:\n%s", nr.Status, issueText(rep, "Mem"))
	}
	validates(t, wf)

	// Without a memory sub-node the item's sessionId is used, and reported.
	src := `{"name":"m","nodes":[{"id":"1","name":"Mem","type":"@n8n/n8n-nodes-langchain.memoryChatRetriever","typeVersion":1,"position":[0,0],"parameters":{"simplifyOutput":false}}],"connections":{}}`
	wf, rep = convertRaw(t, src)
	wantConfig(t, node(t, wf, "Mem"), map[string]interface{}{"format": "raw", "session_key": "{{ $json.sessionId }}"})
	if text := issueText(rep, "Mem"); !strings.Contains(text, "no chat memory is attached") {
		t.Errorf("report:\n%s", text)
	}
}
