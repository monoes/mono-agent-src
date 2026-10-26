package chat

import "testing"

// Saving the settings after an unrelated `coder set` must not store the default of 6 as if
// the operator had chosen it: a mission turn would then stay at 6 workers instead of 12.
func TestCoderSetOfAnotherFlagKeepsOrgMaxAgentsUnset(t *testing.T) {
	withCoderCaps(t)
	dbPath := newChatCLITestDB(t)
	if _, code := runCoderCLI(t, dbPath, "set", "--timeout", "30m"); code != 0 {
		t.Fatalf("set timeout: exit %d", code)
	}
	if s := storedCoderSettings(t, dbPath); !s.orgMaxAgentsDefaulted {
		t.Fatalf("an unrelated set made org-max-agents explicit: %+v", s)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--org-max-agents", "6"); code != 0 {
		t.Fatalf("set org-max-agents: exit %d", code)
	}
	if s := storedCoderSettings(t, dbPath); s.orgMaxAgentsDefaulted || s.OrgMaxAgents != 6 {
		t.Fatalf("an explicit 6 did not stick: %+v", s)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--timeout", "40m"); code != 0 {
		t.Fatal("set timeout again")
	}
	if s := storedCoderSettings(t, dbPath); s.orgMaxAgentsDefaulted || s.OrgMaxAgents != 6 {
		t.Fatalf("an explicit 6 was lost by another set: %+v", s)
	}
}
