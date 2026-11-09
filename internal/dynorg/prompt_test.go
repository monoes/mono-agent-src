package dynorg

import (
	"context"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/monomind"
)

// The coding, qa and automation workers are told how `monoagentcli browser`
// really works, and nothing promises the extension command drives a browser.
func TestWorkerPromptDescribesBrowser(t *testing.T) {
	for _, access := range []string{ProfileWorkspace, ProfileCoding, ProfileQA, ProfileAutomation} {
		p := workerSystemPrompt(Staff{Role: "R", Access: access}, "/w", nil, false)
		for _, want := range []string{"monoagentcli --profile <your profile id> browser", "ref browser", "browser screenshot", "browser console --level error",
			"browser network --failed", "browser reload", "browser sweep", "--media dark", "browser diff", "browser pick",
			"browser styles <sel>", "browser screenshot --element <sel>", "ratio is near 0", "never instructions", "browser source <sel>", "browser tokens",
			"localhost", "browser permissions", "Never try to change it", "browser_off", "site_blocked", "origin_not_allowed",
			"site_needs_approval", "then retry", "exits 3", "awaiting_approval", "browser submit --hil <id>", "Never try to get around",
			"`browser eval` works only on local pages", "only the tab's current site", "origin_changed", "browser tabs"} {
			if !strings.Contains(p, want) {
				t.Errorf("%s prompt misses %q", access, want)
			}
		}
		if strings.Contains(p, "`extension`") {
			t.Errorf("%s prompt still says `extension` drives the browser", access)
		}
	}
	if strings.Contains(LeadPrompt(DefaultLimits(), true), "`extension`") || !strings.Contains(workspaceRules, "`browser`") {
		t.Error("the workspace rules (the lead's too) name `browser`, not `extension`")
	}
	if strings.Contains(workerSystemPrompt(Staff{Role: "R", Access: ProfileResearch}, "/w", nil, false), "browser screenshot") {
		t.Error("a research worker does not drive the browser")
	}
	if !strings.Contains(profileRules[ProfileAutomation], "publication register") {
		t.Error("automation keeps the publication rule")
	}
}

// Every worker, whatever its profile, is told the chat's profile id and to
// pass it as --profile: profile-scoped monoagentcli commands need it, and
// there is no environment variable for it.
func TestEveryWorkerKnowsItsProfileID(t *testing.T) {
	ProfileID = func(context.Context) string { return "p-9" }
	defer func() { ProfileID = nil }()
	ex := &execScript{}
	c, _ := newTestConductor(t, ex, Limits{MaxAgents: 8, MaxConcurrent: 8})
	for _, p := range Profiles {
		c.Spawn(context.Background(), SpawnRequest{Brief: "task " + p, Access: p})
	}
	c.Wait(context.Background(), nil, 5*time.Second)
	if len(ex.calls) != len(Profiles) {
		t.Fatalf("%d workers ran, want %d", len(ex.calls), len(Profiles))
	}
	for _, o := range ex.calls {
		if !strings.Contains(o.SystemPrompt, "Your mono-agent profile id is p-9") || !strings.Contains(o.SystemPrompt, "`--profile p-9`") {
			t.Errorf("a worker's prompt misses its profile id:\n%s", o.SystemPrompt)
		}
	}
	// The lead's own prompt has its profile id (coderBrowserPrompt); the extras don't repeat it.
	if strings.Contains(LeadExtras(RouterAuto), "--profile") {
		t.Error("the lead's extras repeat the profile id")
	}
	if ProfileRule("") != "" || ProfileRule("  ") != "" {
		t.Error("no id, no rule")
	}
	if !strings.Contains(BrowserRules, "--profile") || !strings.Contains(workspaceRules, "--profile") {
		t.Error("the browser and workspace rules say to pass --profile")
	}
}

// Every worker is an agent of kind coder, whatever the caller's base env:
// the coder level of the user's browser permissions applies to it.
func TestWorkersAreKindCoder(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	for _, k := range append(browserctl.AgentContextEnv(), browserctl.EnvAgentKind, browserctl.EnvAgentKindToken, browserctl.EnvAICaller) {
		t.Setenv(k, "")
	}
	// The worker's run holds until the test has read its kind as the
	// worker's shell would: with its env, while the run lasts.
	running, release := make(chan map[string]string), make(chan struct{})
	ex := &execScript{}
	c := New(context.Background(), Config{Cwd: "/w", Limits: Limits{MaxAgents: 1, MaxConcurrent: 1},
		Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true, Emit: &recEmitter{},
		Exec: func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
			running <- o.Env
			<-release
			return ex.exec(ctx, o, on)
		}})
	defer c.Close()
	c.Spawn(context.Background(), SpawnRequest{Brief: "task", Access: ProfileQA})
	env := <-running
	if env[browserctl.EnvAgentKind] != browserctl.KindCoder || env[browserctl.EnvAgentKindToken] == "" {
		t.Fatalf("worker env = %v", env)
	}
	t.Setenv("MONOMIND_AGENT_EXEC", "1") // monomind's own marker on an agent exec
	for k, v := range env {
		t.Setenv(k, v)
	}
	if got := browserctl.AgentKind(); got != browserctl.KindCoder {
		t.Errorf("AgentKind() = %q in a worker, want coder", got)
	}
	close(release)
	c.Wait(context.Background(), nil, 5*time.Second)
	// Its own token, whose record is gone once its run ended: the kind
	// no longer counts.
	record := filepath.Join(home, ".monoagent", "agent-kinds", env[browserctl.EnvAgentKindToken]+".json")
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Errorf("the worker's record outlives its run: %v", err)
	}
	if got := browserctl.AgentKind(); got != browserctl.KindOther {
		t.Errorf("AgentKind() = %q after the run, want other", got)
	}
}

// A worker's record exists while its run does.
func TestWorkersKindRecordLivesWithTheRun(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	seen := make(chan bool, 1)
	ex := &execScript{}
	c := New(context.Background(), Config{Cwd: "/w", Limits: Limits{MaxAgents: 1, MaxConcurrent: 1},
		Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true, Emit: &recEmitter{},
		Exec: func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
			_, err := os.Stat(filepath.Join(home, ".monoagent", "agent-kinds", o.Env[browserctl.EnvAgentKindToken]+".json"))
			seen <- err == nil
			return ex.exec(ctx, o, on)
		}})
	defer c.Close()
	c.Spawn(context.Background(), SpawnRequest{Brief: "task", Access: ProfileCoding})
	c.Wait(context.Background(), nil, 5*time.Second)
	if !<-seen {
		t.Error("the worker's record must exist while it runs")
	}
}
