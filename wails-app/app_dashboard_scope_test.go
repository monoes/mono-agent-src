package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDashboardGlobalBindingsShellOut(t *testing.T) {
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	// org summary first: its argv also contains " summary --all-profiles".
	script := `#!/bin/sh
echo "$*" >> '` + argsLog + `'
case "$*" in
  *" org summary --all-profiles"*) echo '{"scope":"global","orgs":[],"totals":{"orgs":0}}';;
  *" summary --all-profiles"*) echo '{"scope":"global","profiles":[{"id":"default","name":"Default","current":true}]}';;
  *" workflow list --all-profiles"*) echo '[{"id":"w2","name":"B","is_active":true,"profile_id":"p-work","node_count":3,"profile_name":"Work"}]';;
  *" workflow executions --all --all-profiles --limit 30"*) echo '[{"id":"e1","workflow_id":"w2","status":"SUCCESS","profile_id":"p-work","profile_name":"Work"}]';;
  *) echo 'unexpected' >&2; exit 2;;
esac
`
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)

	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("default")

	if s := a.GetGlobalSummary(); !strings.Contains(s, `"scope":"global"`) || !strings.Contains(s, `"profiles"`) {
		t.Fatalf("GetGlobalSummary = %s", s)
	}
	if s := a.GetGlobalOrgSummary(true); !strings.Contains(s, `"orgs":[]`) {
		t.Fatalf("GetGlobalOrgSummary = %s", s)
	}
	wfs, err := a.ListAllProfilesWorkflows()
	if err != nil || len(wfs) != 1 || wfs[0].ProfileID != "p-work" || wfs[0].ProfileName != "Work" || wfs[0].NodeCount != 3 {
		t.Fatalf("ListAllProfilesWorkflows = %+v, %v", wfs, err)
	}
	ex, err := a.GetAllProfilesRecentExecutions(30)
	if err != nil || len(ex) != 1 || ex[0].ProfileName != "Work" {
		t.Fatalf("GetAllProfilesRecentExecutions = %+v, %v", ex, err)
	}
	log, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(log), "org summary --all-profiles --fast") {
		t.Errorf("argv log:\n%s", log)
	}
}
