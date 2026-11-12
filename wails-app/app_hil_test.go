package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMonoCLI installs a monoagentcli stand-in (MONOAGENTCLI_BIN) that
// appends its arguments to a log and prints stdout for `list` calls.
func fakeMonoCLI(t *testing.T, stdout string) (argsLog string) {
	t.Helper()
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	out := filepath.Join(dir, "out.json")
	if err := os.WriteFile(out, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$*\" >> '" + argsLog + "'\ncase \"$*\" in *\" list\"*) cat '" + out + "';; *) echo '{}';; esac\n"
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	return argsLog
}

func loggedArgs(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// The HIL page's Go functions shell out to `monoagentcli hil …` (plan D11)
// instead of querying hil_pending directly.
func TestHILFuncsShellOutToCLI(t *testing.T) {
	log := fakeMonoCLI(t, `[{"id":"h1","workflow_name":"WF","status":"pending",
		"readonly_data":{"to":"a"},"editable_data":{"caption":"x"},
		"node_config":{"suggestion":{"choice":"approve","p":0.97,"risk":"low"}},"created_at":"2026-09-25 10:00:00",
		"suggestion":{"choice":"approve","p":0.97,"risk":"low"}}]`)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	items, err := a.GetHILItems()
	if err != nil || len(items) != 1 || items[0].ID != "h1" || items[0].WorkflowName != "WF" || items[0].EditableData["caption"] != "x" {
		t.Fatalf("GetHILItems = %+v, %v", items, err)
	}
	sug, _ := items[0].NodeConfig["suggestion"].(map[string]interface{})
	if sug["choice"] != "approve" {
		t.Fatalf("suggestion not passed through node_config: %+v", items[0].NodeConfig)
	}
	if err := a.resolveHIL("h1", `{"caption":"y"}`, true); err != nil {
		t.Fatal(err)
	}
	if err := a.resolveHIL("h1", "", false); err != nil {
		t.Fatal(err)
	}
	if err := a.resolveHIL("h1", "{not json", true); err == nil {
		t.Fatal("invalid JSON must be refused before the CLI runs")
	}
	want := []string{
		"--profile work --json hil list --suggest",
		`--profile work --json hil approve h1 --data {"caption":"y"}`,
		"--profile work --json hil reject h1",
	}
	if got := loggedArgs(t, log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestPendingPeopleApprovalsCarrySuggestion(t *testing.T) {
	log := fakeMonoCLI(t, `[{"id":"p1","full_name":"Sam","suggestion":{"suggest":"approve","p":0.93,"intro_fit":"on_topic","intro_fit_p":0.9}}]`)
	a := newTestApp(t)
	a.ctx = context.Background()
	got, err := a.GetPendingPeopleApprovals()
	if err != nil || len(got) != 1 || got[0].Suggestion["intro_fit"] != "on_topic" {
		t.Fatalf("GetPendingPeopleApprovals = %+v, %v", got, err)
	}
	if args := loggedArgs(t, log); args[0] != "--profile default --json people review list --suggest" {
		t.Fatalf("args = %v", args)
	}
}
