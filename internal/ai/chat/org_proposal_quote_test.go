package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

// The revoke preview still prints a command the user may copy: an agent-supplied name stays one inert quoted word.
// A proposal prints no command at all, and a name that is not a name stores nothing.
func TestOrgRevokeCommandQuotesAgentSuppliedNames(t *testing.T) {
	f := newProposalToolFixture(t)
	for _, bad := range []string{"x; rm -r x", "a`id`b", "$(touch x)", "it's", "a b"} {
		q := "'" + strings.ReplaceAll(bad, "'", `'\''`) + "'"
		b, _ := json.Marshal(map[string]interface{}{"org_name": bad, "role_id": bad, "automation": bad, "revoke": true})
		out, err := f.mt.Execute("set_org_grant", string(b))
		if err != nil {
			t.Fatal(err)
		}
		var res struct{ Command string }
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatal(err)
		}
		if want := "monoagentcli org grant remove " + q + " --role " + q + " --automation " + q; res.Command != want {
			t.Errorf("revoke %q: command %q, want %q", bad, res.Command, want)
		}
		for tool, a := range map[string]map[string]interface{}{
			"set_org_grant":    {"org_name": bad, "role_id": bad, "automation": bad},
			"set_org_autonomy": {"org_name": bad, "level": "mid"},
		} {
			b, _ := json.Marshal(a)
			if out, err := f.mt.Execute(tool, string(b)); err == nil {
				t.Errorf("%s over the name %q was not refused: %s", tool, bad, out)
			}
		}
	}
	if n := f.count(t, "org_proposals"); n != 0 {
		t.Errorf("%d proposals stored", n)
	}
	// Plain names stay readable.
	out, err := f.mt.Execute("set_org_grant", `{"org_name":"growth","role_id":"lead","automation":"publish_post","revoke":true}`)
	var res struct{ Command string }
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || res.Command != "monoagentcli org grant remove growth --role lead --automation publish_post" {
		t.Errorf("plain names: %q (%v)", out, err)
	}
}
