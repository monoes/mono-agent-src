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
  *" extension browsers"*) echo '{"running":true,"browsers":[{"instance":"3f2a91c0-0000-4000-8000-000000000001","label":"Edge Work","profile_id":"p-work","profile_name":"Work","legacy":false,"conflict":false,"version":"1.5.0","connected_at":"2026-09-27T14:02:00Z"}]}';;
  *" extension bind "*) echo '{"instance":"3f2a91c0-0000-4000-8000-000000000001","label":"Edge Work","profile_id":"p-home","profile_name":"Personal"}';;
  *" extension pair --open"*) echo '{"opened":false,"fallback":"restart_bridge","hint":"Restart the bridge","error":"mismatch"}';;
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
	a.setActiveProfileID("p-work")

	rep, err := a.GetBrowsers()
	if err != nil || !rep.Running || len(rep.Browsers) != 1 || rep.Browsers[0].ProfileName != "Work" {
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
	if _, err := a.BindBrowser("", "p-home"); err == nil {
		t.Fatal("an empty browser id must be refused before shelling out")
	}

	log, _ := os.ReadFile(argsLog)
	for _, want := range []string{"extension browsers", "extension bind 3f2a91c0-0000-4000-8000-000000000001 p-home", "extension unbind 3f2a91c0-0000-4000-8000-000000000001"} {
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
