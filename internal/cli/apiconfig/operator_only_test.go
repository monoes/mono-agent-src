package apiconfig

import (
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// Saving or removing a setting of the API's server is the user's own: it can
// widen where the server listens and what it serves, and an agent passes --yes
// as easily as the user. Anything else is refused (exit 3, operator_only) and
// nothing changes; --dry-run stays open (#845 security review).
func TestAPIConfigChangesAreTheOperatorsOnly(t *testing.T) {
	t.Parallel()
	f := configTest(t)
	saveAt(t, f.db, "max_concurrent=8")
	f.refusal = "CLAUDECODE is set, so an agent is running this command"
	for _, args := range [][]string{
		{"set", "--confinement", "any", "--yes"},
		{"set", "--v1-addr", "0.0.0.0:9443", "--yes"},
		{"set", "--tool-runtimes", "claude,codex,antigravity", "--yes"},
		{"unset", "--all", "--yes"},
	} {
		_, _, err := f.run(t, true, args...)
		var fields interface{ JSONErrorFields() map[string]any }
		if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &fields) || fields.JSONErrorFields()["code"] != "operator_only" {
			t.Errorf("an agent's api config %v: %v", args, err)
		}
	}
	if s := savedAt(t, f.db); s.MaxConcurrent != "8" || s.Confinement != "" || s.V1Addr != "" || s.ToolRuntimes != "" {
		t.Errorf("the refusals changed the saved settings: %+v", s)
	}
	if _, _, err := f.run(t, true, "set", "--confinement", "any", "--dry-run"); err != nil {
		t.Errorf("an agent's dry run: %v", err)
	}

	f.refusal = ""
	if _, _, err := f.run(t, true, "set", "--confinement", "any", "--yes"); err != nil || savedAt(t, f.db).Confinement != "any" {
		t.Errorf("the user's set: %v", err)
	}
}
