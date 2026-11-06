package chat

import (
	"encoding/json"
	"testing"
)

// An agent-supplied name is never run or printed as a command: a proposal prints no command at all, and a name that
// is not a name of the org stores nothing (the revoke too, since it is a proposal like the others).
func TestOrgToolsRefuseAgentSuppliedNamesAndStoreNothing(t *testing.T) {
	f := newProposalToolFixture(t)
	for _, bad := range []string{"x; echo x", "a`id`b", "$(touch x)", "it's", "a b"} {
		for tool, a := range map[string]map[string]interface{}{
			"set_org_grant":        {"org_name": bad, "role_id": bad, "automation": bad},
			"set_org_grant revoke": {"org_name": bad, "role_id": bad, "automation": bad, "revoke": true},
			"set_org_autonomy":     {"org_name": bad, "level": "mid"},
		} {
			b, _ := json.Marshal(a)
			name := "set_org_grant"
			if tool == "set_org_autonomy" {
				name = tool
			}
			if out, err := f.mt.Execute(name, string(b)); err == nil {
				t.Errorf("%s over the name %q was not refused: %s", tool, bad, out)
			}
		}
	}
	if n := f.count(t, "org_proposals"); n != 0 {
		t.Errorf("%d proposals stored", n)
	}
}
