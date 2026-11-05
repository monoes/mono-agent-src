package chat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The proposal's command line is text the user may copy: an agent-supplied name stays one inert quoted word.
func TestOrgProposalCommandQuotesAgentSuppliedNames(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "/bin/monoagentcli", "p1")
	stubRunSelfExec(t, func(ctx context.Context, bin string, args ...string) ([]byte, error) {
		t.Fatalf("a proposal ran the CLI: %v", args)
		return nil, nil
	})
	command := func(tool string, args map[string]string) string {
		t.Helper()
		b, _ := json.Marshal(args)
		out, err := mt.Execute(tool, string(b))
		if err != nil {
			t.Fatal(err)
		}
		var res struct{ Command string }
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatal(err)
		}
		return res.Command
	}
	for _, bad := range []string{"x; rm -rf x", "a`id`b", "$(touch x)", "it's", "a b"} {
		q := "'" + strings.ReplaceAll(bad, "'", `'\''`) + "'"
		want := "monoagentcli org grant add " + q + " --role " + q + " --automation " + q
		if got := command("set_org_grant", map[string]string{"org_name": bad, "role_id": bad, "automation": bad}); got != want {
			t.Errorf("grant %q: command %q, want %q", bad, got, want)
		}
		want = "monoagentcli org autonomy set " + q + " --by chat --level mid"
		if got := command("set_org_autonomy", map[string]string{"org_name": bad, "level": "mid"}); got != want {
			t.Errorf("autonomy %q: command %q, want %q", bad, got, want)
		}
	}
	// Plain names stay readable.
	if got := command("set_org_grant", map[string]string{"org_name": "growth", "role_id": "lead", "automation": "publish_post"}); got !=
		"monoagentcli org grant add growth --role lead --automation publish_post" {
		t.Errorf("plain names: %q", got)
	}
}
