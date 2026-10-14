package n8nimport_test

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/n8nimport"
	"github.com/monoes/mono-agent/internal/workflow"
)

const sendAndWaitWorkflow = `{"name":"approvals","nodes":[
 {"id":"1","name":"Start","type":"n8n-nodes-base.manualTrigger","typeVersion":1,"position":[0,0],"parameters":{}},
 {"id":"2","name":"Ask Slack","type":"n8n-nodes-base.slack","typeVersion":2.3,"position":[200,0],
  "parameters":{"operation":"sendAndWait","user":{"__rl":true,"mode":"id","value":"U123"},"message":"=Approve {{ $json.name }}?",
   "approvalOptions":{"values":{"approvalType":"double"}},
   "options":{"limitWaitTime":{"values":{"resumeAmount":2,"resumeUnit":"hours"}}}},
  "credentials":{"slackApi":{"id":"c1","name":"Slack"}}},
 {"id":"3","name":"Ask Gmail","type":"n8n-nodes-base.gmail","typeVersion":2.1,"position":[400,0],
  "parameters":{"operation":"sendAndWait","sendTo":"boss@example.com","subject":"Approve?","message":"Please approve","responseType":"freeText"}},
 {"id":"4","name":"Ask Teams","type":"n8n-nodes-base.microsoftTeams","typeVersion":2,"position":[600,0],
  "parameters":{"resource":"chatMessage","operation":"sendAndWait","chatId":{"__rl":true,"mode":"id","value":"19:abc"},"message":"Go?",
   "options":{"limitWaitTime":{"values":{"limitType":"atSpecifiedTime","maxDateAndTime":"2026-12-01T00:00:00"}}}}},
 {"id":"5","name":"After","type":"n8n-nodes-base.noOp","typeVersion":1,"position":[800,0],"parameters":{}}
],"connections":{
 "Start":{"main":[[{"node":"Ask Slack","type":"main","index":0}]]},
 "Ask Slack":{"main":[[{"node":"Ask Gmail","type":"main","index":0}]]},
 "Ask Gmail":{"main":[[{"node":"Ask Teams","type":"main","index":0}]]},
 "Ask Teams":{"main":[[{"node":"After","type":"main","index":0}]]}
}}`

func nodeByName(t *testing.T, wf *workflow.WorkflowFile, name string) workflow.WorkflowFileNode {
	t.Helper()
	for _, n := range wf.Nodes {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("no node %q", name)
	return workflow.WorkflowFileNode{}
}

func sendWaitEdge(wf *workflow.WorkflowFile, from, to string) bool {
	for _, e := range wf.Connections {
		if e.Source == from && e.Target == to {
			return true
		}
	}
	return false
}

func issuesOf(rep *n8nimport.Report, name string) string {
	var b strings.Builder
	for _, n := range rep.Nodes {
		if n.Name == name {
			for _, i := range n.Issues {
				b.WriteString(i.Param + ": " + i.Message + "\n")
			}
		}
	}
	return b.String()
}

func TestSendAndWaitBecomesSendPlusReview(t *testing.T) {
	wf, rep, err := n8nimport.Convert([]byte(sendAndWaitWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, sendType string
		sendCfg        map[string]interface{}
		timeout        float64
		issues         []string
	}{
		{"Ask Slack", "comm.slack", map[string]interface{}{"operation": "post_message", "channel": "U123", "text": "Approve {{ $json.name }}?"}, 120,
			[]string{"became message.post followed by a human review step", "Slack message has no approve/decline buttons"}},
		{"Ask Gmail", "", map[string]interface{}{"subject": "Approve?"}, 0,
			[]string{"became message.send", "responseType: the review step only approves or rejects"}},
		{"Ask Teams", "app.microsoft_teams", map[string]interface{}{"resource": "chatMessage", "operation": "create", "message": "Go?", "contentType": "text"}, 0,
			[]string{"became chatMessage.create", "waiting until a date is not supported"}},
	}
	for _, c := range cases {
		send := nodeByName(t, wf, c.name+" (send)")
		review := nodeByName(t, wf, c.name)
		if c.sendType != "" && send.Type != c.sendType {
			t.Errorf("%s: send type %s, want %s", c.name, send.Type, c.sendType)
		}
		for k, v := range c.sendCfg {
			if got := send.Config[k]; jsonString(got) != jsonString(v) {
				t.Errorf("%s: send config[%s] = %#v, want %#v", c.name, k, got, v)
			}
		}
		for _, k := range []string{"approvalOptions", "responseType", "limitWaitTime"} {
			if _, ok := send.Config[k]; ok {
				t.Errorf("%s: sendAndWait param %s leaked into the send node", c.name, k)
			}
		}
		if review.Type != "core.human_in_loop" || review.Config["timeout_minutes"] != c.timeout {
			t.Errorf("%s: review %s %v", c.name, review.Type, review.Config)
		}
		if !sendWaitEdge(wf, send.ID, review.ID) {
			t.Errorf("%s: send is not wired to the review step", c.name)
		}
		got := issuesOf(rep, c.name)
		for _, want := range c.issues {
			if !strings.Contains(got, want) {
				t.Errorf("%s: report lacks %q:\n%s", c.name, want, got)
			}
		}
		if strings.Contains(got, "not used") || strings.Contains(got, "approvalOptions") {
			t.Errorf("%s: sendAndWait params reported as unused:\n%s", c.name, got)
		}
	}
	gm := nodeByName(t, wf, "Ask Gmail (send)")
	if gm.Config["to"] != "boss@example.com" || gm.Config["body"] != "Please approve" || gm.Config["body_type"] != "text" {
		t.Errorf("gmail send config %v", gm.Config)
	}
	if !sendWaitEdge(wf, nodeByName(t, wf, "Ask Slack").ID, gm.ID) || !sendWaitEdge(wf, nodeByName(t, wf, "Ask Teams").ID, nodeByName(t, wf, "After").ID) {
		t.Errorf("chain is not wired in order: %+v", wf.Connections)
	}
}
