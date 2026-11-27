package org

import (
	"strings"
	"testing"
)

// `org chat approve`, what the bubble card runs, releases only a request it
// can name: without a request id it refuses, says why and what to do instead,
// and sends nothing. Denying works as before.
func TestOrgChatApproveNeedsARequestID(t *testing.T) {
	cfg, fake, _ := setupOrgChat(t)
	asOrgOperator(t)
	chatApprovals(fake, map[string]interface{}{"roleId": "dev", "action": "Bash", "question": "q", "ts": 1700, "approved": nil,
		"input": map[string]interface{}{"command": "ls"}})
	for _, ref := range []string{"dev:Bash:1700", "dev:Bash"} {
		_, err := runOrgChat(t, cfg, "approve", "acme", ref)
		if exitCodeFor(err) != 3 {
			t.Fatalf("approve %s: %v", ref, err)
		}
		for _, want := range []string{"no request id", "nothing was sent", "update monomind", "org approve acme dev Bash"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("approve %s: %q does not say %q", ref, err, want)
			}
		}
	}
	if len(fake.sent) != 0 {
		t.Fatalf("sent %v", fake.sent)
	}
	if _, err := runOrgChat(t, cfg, "deny", "acme", "dev:Bash:1700"); err != nil || len(fake.sent) != 1 {
		t.Fatalf("deny: %v, sent %v", err, fake.sent)
	}
	// With ids, a role:action ref is refused too and names the request.
	fake.sent = nil
	chatApprovals(fake, map[string]interface{}{"roleId": "dev", "action": "Bash", "question": "q", "ts": 1800, "approved": nil, "requestId": "apr-2"})
	if _, err := runOrgChat(t, cfg, "approve", "acme", "dev:Bash"); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "apr-2") || len(fake.sent) != 0 {
		t.Fatalf("approve by role:action: %v, sent %v", err, fake.sent)
	}
	if _, err := runOrgChat(t, cfg, "approve", "acme", "apr-2"); err != nil || len(fake.sent) != 1 {
		t.Fatalf("approve by id: %v, sent %v", err, fake.sent)
	}
}
