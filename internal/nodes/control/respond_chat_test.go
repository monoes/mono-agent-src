package control

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// chatFlow saves and activates trigger.chat at path → nodes in a chain.
func chatFlow(t *testing.T, path, mode string, nodes ...workflow.WorkflowNode) (*subflowFixture, string) {
	t.Helper()
	testhome.SetHome(t, t.TempDir()) // the reply key file
	t.Setenv("MONOAGENT_PUBLIC_URL", "https://chat.example.com")
	f := newSubflowFixture(t)
	ctx := context.Background()
	id := "chat-" + path
	wf := &workflow.Workflow{ID: id, Name: id, ProfileID: "default", Nodes: []workflow.WorkflowNode{
		{ID: "t", WorkflowID: id, Type: workflow.TriggerNodeTypeChat, Name: "Chat Trigger",
			Config: map[string]interface{}{"path": path, "title": "Help desk", "responseMode": mode}}}}
	prev := "t"
	for i, n := range nodes {
		n.ID, n.WorkflowID = "n"+string(rune('0'+i)), id
		wf.Nodes = append(wf.Nodes, n)
		wf.Connections = append(wf.Connections, workflow.WorkflowConnection{SourceNodeID: prev, SourceHandle: "main", TargetNodeID: n.ID, TargetHandle: "main"})
		prev = n.ID
	}
	if err := f.eng.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.ActivateWorkflow(ctx, id); err != nil {
		t.Fatal(err)
	}
	return f, id
}

var chatSessions atomic.Int64

// chatSession is a session id no earlier run of the test used: the chat
// messages live in a store of the process, so a fixed id would find the
// messages of the previous iteration under -count.
func chatSession(base string) string {
	return fmt.Sprintf("%s-%d", base, chatSessions.Add(1))
}

// say posts a chat message and returns the response body.
func say(t *testing.T, f *subflowFixture, path, session, text string) map[string]interface{} {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"action": "sendMessage", "sessionId": session, "chatInput": text})
	req := httptest.NewRequest(http.MethodPost, "/chat/"+path+"/message", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := f.serve(req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("message %q = %d %s", text, rec.Code, rec.Body.String())
	}
	var out map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// botSays waits for the session's next bot message after *seen.
func botSays(t *testing.T, session string, seen *int) workflow.ChatMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range workflow.ChatMessagesAfter(context.Background(), session, *seen, time.Second) {
			*seen = m.ID
			if m.Role == "bot" {
				return m
			}
		}
	}
	t.Fatalf("no bot message in session %s", session)
	return workflow.ChatMessage{}
}

func TestChat_LastNodeAnswers(t *testing.T) {
	f, _ := chatFlow(t, "echo", "lastNode", workflow.WorkflowNode{Type: "core.code", Name: "Echo",
		Config: map[string]interface{}{"code": `return [{ json: { output: "echo: " + $json.chatInput + " (" + $json.sessionId + ")" } }]`}})
	page := f.serve(httptest.NewRequest(http.MethodGet, "/chat/echo", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Help desk") ||
		!strings.Contains(page.Header().Get("Content-Security-Policy"), "script-src 'nonce-") {
		t.Fatalf("page = %d %v", page.Code, page.Header())
	}
	seen := 0
	s1 := chatSession("session-one")
	say(t, f, "echo", s1, "hello")
	if m := botSays(t, s1, &seen); m.Text != "echo: hello ("+s1+")" {
		t.Fatalf("bot = %q", m.Text)
	}
	// The page reads the same messages over HTTP.
	rec := f.serve(httptest.NewRequest(http.MethodGet, "/chat/echo/messages?sessionId="+s1+"&after=0", nil))
	var got struct{ Messages []workflow.ChatMessage }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Messages) != 2 || got.Messages[0].Role != "user" || got.Messages[0].Text != "hello" {
		t.Fatalf("messages = %s", rec.Body.String())
	}
	if rec := f.serve(httptest.NewRequest(http.MethodGet, "/chat/echo/messages?sessionId=x", nil)); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad session = %d", rec.Code)
	}
}

// A respond_chat sendAndWait asks in the chat; the person's next message
// is its answer (no new run), and the run goes on to answer them.
func TestChat_SendAndWaitFreeText(t *testing.T) {
	f, wf := chatFlow(t, "ask", "responseNodes",
		workflow.WorkflowNode{Type: "core.respond_chat", Name: "Ask", Config: map[string]interface{}{
			"operation": "sendAndWait", "message": "What is your name?"}},
		workflow.WorkflowNode{Type: "core.respond_chat", Name: "Greet", Config: map[string]interface{}{
			"message": "Hello {{ $json.chatInput }}"}})
	seen := 0
	s2 := chatSession("session-two")
	first := say(t, f, "ask", s2, "hi")
	if m := botSays(t, s2, &seen); m.Text != "What is your name?" {
		t.Fatalf("bot = %q", m.Text)
	}
	answer := say(t, f, "ask", s2, "Ann")
	if answer["answered"] != true || answer["execution_id"] != first["execution_id"] {
		t.Fatalf("answer = %v (first %v): the reply should resume the waiting run", answer, first)
	}
	if m := botSays(t, s2, &seen); m.Text != "Hello Ann" {
		t.Fatalf("bot = %q", m.Text)
	}
	id, _ := first["execution_id"].(string)
	if st := f.waitTerminal(t, id); st != "SUCCESS" {
		t.Fatalf("run = %s", st)
	}
	if out := chatNodeOutput(t, f, id, "n0"); out["chatInput"] != "Ann" || out["sessionId"] != s2 || out["action"] != "sendMessage" {
		t.Fatalf("answer item = %v", out)
	}
	var runs int
	_ = f.store.RawDB().QueryRow(`SELECT COUNT(*) FROM workflow_executions WHERE workflow_id = ?`, wf).Scan(&runs)
	if runs != 1 {
		t.Fatalf("%d runs, want the answer to resume the first", runs)
	}
}

func TestChat_SendAndWaitApproval(t *testing.T) {
	f, _ := chatFlow(t, "approve", "responseNodes",
		workflow.WorkflowNode{Type: "core.respond_chat", Name: "Confirm", Config: map[string]interface{}{
			"operation": "sendAndWait", "message": "Book it?", "responseType": "approval", "blockUserInput": true,
			"approvalOptions": `{"values": {"approvalType": "double", "approveLabel": "Yes"}}`}})
	seen := 0
	s3 := chatSession("session-three")
	first := say(t, f, "approve", s3, "book a room")
	m := botSays(t, s3, &seen)
	if len(m.Buttons) != 2 || m.Buttons[0].Label != "Yes" || !m.BlockInput {
		t.Fatalf("buttons = %+v", m)
	}
	u, _ := url.Parse(m.Buttons[0].URL)
	if rec := f.serve(httptest.NewRequest(http.MethodPost, u.Path, nil)); rec.Code != http.StatusOK {
		t.Fatalf("approve = %d %s", rec.Code, rec.Body.String())
	}
	id, _ := first["execution_id"].(string)
	if st := f.waitTerminal(t, id); st != "SUCCESS" {
		t.Fatalf("run = %s", st)
	}
	if out := chatNodeOutput(t, f, id, "n0"); out["data"].(map[string]interface{})["approved"] != true {
		t.Fatalf("output = %v", out)
	}
}

func TestChatAnswerItemShapes(t *testing.T) {
	typed := &workflow.WaitReplyResult{Kind: workflow.WaitReplyApproval,
		Answer: map[string]interface{}{"sessionId": "s", "action": "sendMessage", "chatInput": "no"}}
	if j := chatAnswerItem(typed, false).JSON; j["chatInput"] != "no" || j["data"].(map[string]interface{})["approved"] != false {
		t.Fatalf("v1.2 = %v", j)
	}
	if j := chatAnswerItem(typed, true).JSON["data"].(map[string]interface{}); j["chatInput"] != "no" || j["approved"] != false {
		t.Fatalf("v1.1 = %v", j)
	}
}

func TestRespondChatNeedsChatSession(t *testing.T) {
	_, err := (&RespondChatNode{}).Execute(context.Background(), workflow.NodeInput{}, map[string]interface{}{"message": "x"})
	if err == nil || !strings.Contains(err.Error(), "trigger.chat") {
		t.Fatalf("err = %v", err)
	}
}

// chatNodeOutput is a node's first output item in a finished run.
func chatNodeOutput(t *testing.T, f *subflowFixture, execID, nodeID string) map[string]interface{} {
	t.Helper()
	exec, _ := f.store.GetExecution(context.Background(), execID)
	for _, n := range exec.Nodes {
		if n.NodeID == nodeID && n.Status == "SUCCESS" && len(n.OutputItems) > 0 {
			return n.OutputItems[0].JSON
		}
	}
	t.Fatalf("node %s has no output in %+v", nodeID, exec.Nodes)
	return nil
}
