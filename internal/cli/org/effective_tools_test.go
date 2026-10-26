package org

import "testing"

func TestEffectiveToolsListsCoordinatorResetToolsForTheBossOnly(t *testing.T) {
	f := newOrgCLIFixture(t)
	f.mustRun(t, "create-json", "resets", "--json", `{"name":"resets","goal":"g","status":"stopped","schedule":null,"roles":[{"id":"lead","title":"Lead","type":"boss","reports_to":null,"responsibilities":[]},{"id":"dev","title":"Dev","type":"coder","reports_to":"lead","responsibilities":[]}]}`)
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
		if !boss[n] {
			t.Errorf("boss does not list %s", n)
		}
		if worker[n] {
			t.Errorf("non-boss role lists %s", n)
		}
	}
}
