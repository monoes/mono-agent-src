package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeBrowsersCLI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	script := `#!/bin/sh
echo "$*" >> '` + argsLog + `'
case "$*" in
  *" extension browsers"*) echo '{"running":true,"browsers":[{"instance":"3f2a91c0-0000-4000-8000-000000000001","label":"Edge Work","profile_id":"p-work","profile_name":"Work","profile_ids":["p-work"],"profile_names":["Work"],"legacy":false,"conflict":false,"version":"1.5.0","connected_at":"2026-09-27T14:02:00Z"}]}';;
  *" extension bind "*) echo '{"instance":"3f2a91c0-0000-4000-8000-000000000001","label":"Edge Work","profile_id":"p-home","profile_name":"Personal"}';;
  *" extension pair --open"*) echo '{"opened":false,"fallback":"restart_bridge","hint":"Restart the bridge","error":"mismatch"}';;
  *" extension assign "*) echo '{"profile_id":"p-home","instance":"3f2a91c0-0000-4000-8000-000000000001","changed":[{"instance":"3f2a91c0-0000-4000-8000-000000000001","label":"Edge Work","profile_id":"p-work","profile_ids":["p-work","p-home"],"profile_names":["Work","Personal"]}]}';;
  *" extension unbind "*) echo '{"instance":"3f2a91c0-0000-4000-8000-000000000001","label":"Edge Work","profile_id":"","profile_name":""}';;
  *) echo 'unexpected' >&2; exit 2;;
esac
`
	bin := filepath.Join(dir, "monoagentcli")
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	return argsLog
}

func TestBrowserBindingsShellOutToCLI(t *testing.T) {
	argsLog := fakeBrowsersCLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("p-work")

	rep, err := a.GetBrowsers()
	if err != nil || !rep.Running || len(rep.Browsers) != 1 || rep.Browsers[0].ProfileName != "Work" || len(rep.Browsers[0].ProfileIDs) != 1 {
		t.Fatalf("GetBrowsers = %+v, %v", rep, err)
	}
	row, err := a.BindBrowser("3f2a91c0-0000-4000-8000-000000000001", "p-home")
	if err != nil || row.ProfileID != "p-home" {
		t.Fatalf("BindBrowser = %+v, %v", row, err)
	}
	row, err = a.BindBrowser("3f2a91c0-0000-4000-8000-000000000001", "")
	if err != nil || row.ProfileID != "" {
		t.Fatalf("unbind = %+v, %v", row, err)
	}
	rep2, err := a.AssignBrowser("p-home", "3f2a91c0-0000-4000-8000-000000000001")
	if err != nil || len(rep2.Changed) != 1 || len(rep2.Changed[0].ProfileIDs) != 2 || rep2.Changed[0].ProfileNames[1] != "Personal" {
		t.Fatalf("AssignBrowser = %+v, %v", rep2, err)
	}
	if _, err := a.AssignBrowser("p-home", ""); err != nil {
		t.Fatalf("assign to no browser: %v", err)
	}
	if _, err := a.AssignBrowser("", "x"); err == nil {
		t.Fatal("an empty profile must be refused before shelling out")
	}
	if _, err := a.BindBrowser("", "p-home"); err == nil {
		t.Fatal("an empty browser id must be refused before shelling out")
	}

	log, _ := os.ReadFile(argsLog)
	for _, want := range []string{"extension browsers", "extension bind 3f2a91c0-0000-4000-8000-000000000001 p-home", "extension unbind 3f2a91c0-0000-4000-8000-000000000001", "extension assign p-home 3f2a91c0-0000-4000-8000-000000000001"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("argv log missing %q:\n%s", want, log)
		}
	}
}

func TestPairExtensionShellsOutToCLI(t *testing.T) {
	argsLog := fakeBrowsersCLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()

	out := a.PairExtension()
	if !strings.Contains(out, `"fallback":"restart_bridge"`) {
		t.Fatalf("PairExtension = %s", out)
	}
	log, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(log), "--json extension pair --open") {
		t.Errorf("argv log:\n%s", log)
	}
}

// An id or browser that starts with '-' would be read by the CLI as a flag.
func TestBrowserCallsRefuseFlagLikeArguments(t *testing.T) {
	argsLog := fakeBrowsersCLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("p-work")

	if _, err := a.AssignBrowser("--json", "x"); err == nil {
		t.Error("AssignBrowser accepted a profile that starts with '-'")
	}
	if _, err := a.AssignBrowser("p-home", "--force"); err == nil {
		t.Error("AssignBrowser accepted an instance that starts with '-'")
	}
	if _, err := a.BindBrowser("-x", "p-home"); err == nil {
		t.Error("BindBrowser accepted an instance that starts with '-'")
	}
	if _, err := a.BindBrowser("3f2a91c0-0000-4000-8000-000000000001", "--all"); err == nil {
		t.Error("BindBrowser accepted a profile that starts with '-'")
	}
	if log, _ := os.ReadFile(argsLog); strings.Contains(string(log), " -") {
		t.Errorf("a flag-like argument reached the CLI:\n%s", log)
	}
}
