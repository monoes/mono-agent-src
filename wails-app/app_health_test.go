package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHealthArgs(t *testing.T) {
	for mode, want := range map[string][]string{
		// The background check never scans runtimes (#146 item 1).
		"background": {"--profile", "p1", "--json", "doctor", "--skip-group", "runtimes"},
		"local":      {"--profile", "p1", "--json", "doctor"},
		"deep":       {"--profile", "p1", "--json", "doctor", "--deep"},
		"projects":   {"--profile", "p1", "--json", "doctor", "--projects"},
	} {
		if got, err := healthArgs("p1", mode); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("healthArgs(%s) = %v, %v; want %v", mode, got, err, want)
		}
	}
	if _, err := healthArgs("", "local"); !errors.Is(err, errNoProfile) {
		t.Fatalf("no profile: %v, want errNoProfile", err)
	}
	if _, err := healthArgs("p1", "--deep"); err == nil {
		t.Fatal("an unknown mode must be refused")
	}
	if got, want := mustArgs(t)(healthFixArgs("p1", "runtimes.install:claude")), []string{"--profile", "p1", "--json", "doctor", "fix", "--", "runtimes.install:claude"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("healthFixArgs = %v, want %v", got, want)
	}
}

func TestAgentInstallArgs(t *testing.T) {
	// No --yes: a script install approves only the URL the person was shown.
	if got, want := mustArgs(t)(agentInstallArgs("p1", "hermes", true, "https://hermes.example/install.sh")), []string{"--profile", "p1", "--json", "agent", "install", "--force", "--approve-script", "https://hermes.example/install.sh", "--", "hermes"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("agentInstallArgs = %v, want %v", got, want)
	}
	if _, err := agentInstallArgs("", "codex", false, ""); !errors.Is(err, errNoProfile) {
		t.Fatalf("no profile: %v, want errNoProfile", err)
	}
}

func TestHealthReportJSONKeepsReportOnExit1(t *testing.T) {
	report := `{"v":1,"results":[]}`
	if got := healthReportJSON("monoagentcli", []byte(report+"\n"), errors.New("exit status 1")); got != report {
		t.Fatalf("report on a failing exit must be returned verbatim, got %s", got)
	}
	got := healthReportJSON("monoagentcli", []byte("Usage: monoagentcli …"), nil)
	if !strings.Contains(got, `"error"`) {
		t.Fatalf("non-report output must become an error, got %s", got)
	}
}

func mustHealthArgs(t *testing.T, profile, mode string) []string {
	t.Helper()
	args, err := healthArgs(profile, mode)
	if err != nil {
		t.Fatal(err)
	}
	return args
}

// The tile install and the health runtime fix share one key (#146 item 8).
func TestRunKeySharedPerRuntime(t *testing.T) {
	if runKey("agent.install:codex") != runKey("runtimes.install:codex") {
		t.Fatal("agent.install and runtimes.install of one runtime must share a key")
	}
	if runKey("agent.install:codex") == runKey("agent.install:claude") {
		t.Fatal("different runtimes must not share a key")
	}
	if runKey("core.db.migrate") != "core.db.migrate" {
		t.Fatal("other fixes keep their id as key")
	}
	run, _, ok := beginHealthRun(context.Background(), runKey("runtimes.install:hermes"), time.Minute)
	if !ok {
		t.Fatal("first run must start")
	}
	defer endHealthRun(runKey("runtimes.install:hermes"), run)
	if _, _, ok := beginHealthRun(context.Background(), runKey("agent.install:hermes"), time.Minute); ok {
		t.Fatal("a tile install must be refused while the runtime fix runs")
	}
	a := &App{}
	if got := a.InstallAgentRuntime("hermes", false, ""); !strings.Contains(got, "already") && !strings.Contains(got, "error") {
		t.Fatalf("InstallAgentRuntime while the fix runs = %s", got)
	}
}

func TestRefreshesPath(t *testing.T) {
	for id, want := range map[string]bool{
		"monomind.node.install": true, "monomind.node.update": true, "monomind.install": true,
		"runtimes.install:codex": true, "agent.install:claude": true,
		"core.db.migrate": false, "services.daemon.start": false,
	} {
		if refreshesPath(id) != want {
			t.Errorf("refreshesPath(%s) = %v, want %v", id, !want, want)
		}
	}
}

func TestCancelHealthRunWithNothingRunning(t *testing.T) {
	if got := (&App{}).CancelHealthRun("check:local"); got != `{"ok":true,"cancelled":false}` {
		t.Fatalf("CancelHealthRun = %s", got)
	}
}
