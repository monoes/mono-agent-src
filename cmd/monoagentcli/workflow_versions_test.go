package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func versionsDoc(id, value string) string {
	idField := ""
	if id != "" {
		idField = `"id":"` + id + `",`
	}
	return `{` + idField + `"name":"versions-e2e","nodes":[
	  {"id":"vt","node_type":"trigger.manual","name":"T","config":{}},
	  {"id":"vs","node_type":"core.set","name":"S","config":{"value":"` + value + `","assignments":[{"field":"x","value":"1"}]}}],
	  "connections":[{"id":"vc","source_node_id":"vt","source_handle":"main","target_node_id":"vs","target_handle":"main"}]}`
}

func listVersions(t *testing.T, cfg *globalConfig, id string) []versionRow {
	t.Helper()
	out, err := runWorkflowCmdIn(t, cfg, "", "versions", id)
	if err != nil {
		t.Fatalf("workflow versions: %v", err)
	}
	var rows []versionRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("versions output %q: %v", out, err)
	}
	return rows
}

func rowVersions(rows []versionRow) []int {
	var out []int
	for _, r := range rows {
		out = append(out, r.Version)
	}
	return out
}

// The editor saves a workflow repeatedly; `workflow versions` lists the
// distinct definitions, `show` prints one, `restore` brings it back as a new
// version, and `prune` trims the history.
func TestWorkflowVersionsCLI(t *testing.T) {
	_, cfg := persistTestEnv(t)
	wf := saveDoc(t, cfg, versionsDoc("", "one"))
	saveDoc(t, cfg, versionsDoc(wf.ID, "one")) // unchanged: no new version
	saveDoc(t, cfg, versionsDoc(wf.ID, "two"))
	saveDoc(t, cfg, versionsDoc(wf.ID, "three"))

	rows := listVersions(t, cfg, wf.ID)
	if got := rowVersions(rows); len(got) != 3 || got[0] != 3 || !rows[0].Current || rows[1].Current {
		t.Fatalf("versions %+v, want 3 (current), 2, 1", rows)
	}
	if rows[0].Nodes != 2 || rows[0].Connections != 1 {
		t.Errorf("version 3 has %d nodes, %d connections", rows[0].Nodes, rows[0].Connections)
	}

	out, err := runWorkflowCmdIn(t, cfg, "", "versions", "show", wf.ID, "1")
	if err != nil || !strings.Contains(out, `"value":"one"`) {
		t.Fatalf("show 1 = %q, %v", out, err)
	}

	out, err = runWorkflowCmdIn(t, cfg, "", "versions", "restore", wf.ID, "1")
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	var restored struct {
		Version      int `json:"version"`
		RestoredFrom int `json:"restored_from"`
	}
	if err := json.Unmarshal([]byte(out), &restored); err != nil || restored.Version != 4 || restored.RestoredFrom != 1 {
		t.Fatalf("restore output %q (%v)", out, err)
	}
	out, err = runWorkflowCmdIn(t, cfg, "", "get", wf.ID)
	if err != nil || !strings.Contains(out, `"value": "one"`) {
		t.Fatalf("after restore, get = %q, %v", out, err)
	}
	rows = listVersions(t, cfg, wf.ID)
	if rows[0].Version != 4 || rows[0].Note != "restored from version 1" {
		t.Errorf("newest version %+v", rows[0])
	}

	if _, err := runWorkflowCmdIn(t, cfg, "", "versions", "prune", wf.ID, "--keep", "2"); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if got := rowVersions(listVersions(t, cfg, wf.ID)); len(got) != 2 || got[0] != 4 || got[1] != 3 {
		t.Errorf("after prune: versions %v, want [4 3]", got)
	}
}

func TestWorkflowVersionsCLIErrors(t *testing.T) {
	_, cfg := persistTestEnv(t)
	wf := saveDoc(t, cfg, versionsDoc("", "one"))

	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"versions", "no-such-workflow"}, 2},
		{[]string{"versions", "show", wf.ID, "99"}, 2},
		{[]string{"versions", "show", wf.ID, "zero"}, 3},
		{[]string{"versions", "restore", wf.ID, "7"}, 2},
		{[]string{"versions", "prune", "--keep", "0", wf.ID}, 3},
		{[]string{"versions", "prune", "--keep", "1"}, 3},
	} {
		_, err := runWorkflowCmdIn(t, cfg, "", tc.args...)
		ce, ok := err.(*cliError)
		if !ok || ce.Code != tc.code {
			t.Errorf("%v: err = %v, want exit code %d", tc.args, err, tc.code)
		}
	}

	// Another profile cannot see the history.
	if _, err := runWorkflowCmdIn(t, otherProfileCfg(t, cfg), "", "versions", wf.ID); err == nil {
		t.Error("another profile listed the versions")
	}
}
