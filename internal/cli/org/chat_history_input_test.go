package org

import (
	"encoding/json"
	"testing"
)

func chatApprovals(fake *chatFake, items ...map[string]interface{}) {
	fake.lists["approvals"], _ = json.Marshal(map[string]interface{}{"v": 1, "org": "acme", "items": items})
}

// `org chat history` carries what the card needs to show the whole call.
func TestOrgChatHistoryCarriesTheCallsInput(t *testing.T) {
	cfg, fake, _ := setupOrgChat(t)
	chatApprovals(fake, map[string]interface{}{"roleId": "dev", "action": "Bash", "question": "q", "ts": 1700, "approved": nil, "requestId": "apr-1",
		"input": map[string]interface{}{"command": "ls", "dangerouslyDisableSandbox": true}})
	out, err := runOrgChat(t, cfg, "history", "acme")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Items []struct {
			Ref       string                 `json:"ref"`
			RequestID string                 `json:"request_id"`
			Input     map[string]interface{} `json:"input"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	for _, it := range got.Items {
		if it.Ref == "apr-1" {
			if it.RequestID != "apr-1" || it.Input["command"] != "ls" || it.Input["dangerouslyDisableSandbox"] != true {
				t.Fatalf("approval = %+v", it)
			}
			return
		}
	}
	t.Fatalf("no approval in %s", out)
}
