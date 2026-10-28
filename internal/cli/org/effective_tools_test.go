package org

import "testing"

// The reset tools are opt-in in monomind (run_config.max_role_resets above 0), so
// the list shows them only for such an org, and only to its boss.
func TestEffectiveToolsListsCoordinatorResetToolsOnlyWhenTheOrgOptedIn(t *testing.T) {
	roles := `"roles":[{"id":"lead","title":"Lead","type":"boss","reports_to":null,"responsibilities":[]},{"id":"dev","title":"Dev","type":"coder","reports_to":"lead","responsibilities":[]}]`
	for _, tc := range []struct {
		name, runConfig string
		want            bool
	}{
		{"unset", "", false},
		{"zero", `"run_config":{"max_role_resets":0},`, false},
		{"not a number", `"run_config":{"max_role_resets":"10"},`, false},
		{"opted in", `"run_config":{"max_role_resets":10},`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrgCLIFixture(t)
			f.mustRun(t, "create-json", "resets", "--json", `{"name":"resets","goal":"g","status":"stopped","schedule":null,`+tc.runConfig+roles+`}`)
			tools := func(role string) map[string]bool {
				out := f.mustRun(t, "effective-tools", "resets", "--role", role)
				names := map[string]bool{}
				for _, it := range out["tools"].([]interface{}) {
					names[it.(map[string]interface{})["name"].(string)] = true
				}
				return names
			}
			boss, worker := tools("lead"), tools("dev")
			for _, n := range []string{"org_compact_role", "org_clear_role"} {
				if boss[n] != tc.want {
					t.Errorf("boss lists %s = %v, want %v", n, boss[n], tc.want)
				}
				if worker[n] {
					t.Errorf("non-boss role lists %s", n)
				}
			}
			if !boss["org_respawn_role"] {
				t.Error("the boss lost org_respawn_role")
			}
		})
	}
}
