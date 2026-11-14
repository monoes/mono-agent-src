package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/accountcli"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/daemon"
	doctorcli "github.com/monoes/mono-agent/internal/cli/doctor"
	"github.com/monoes/mono-agent/internal/cli/librarycli"
	logincli "github.com/monoes/mono-agent/internal/cli/login"
	"github.com/monoes/mono-agent/internal/cli/releasecli"
	"github.com/monoes/mono-agent/internal/cli/update"
	"github.com/monoes/mono-agent/internal/release"
)

// Class C of issue 1475, as the owner decided it: the verbs of operatorOnlyRowsPR6 are refused in every
// context that is not the operator's own terminal, and the verbs left open stay open.

// Each flipped verb is refused in each agent context, before the database is read.
func TestClassCFlippedVerbsAreRefusedInEveryAgentContext(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		for _, r := range operatorOnlyRowsPR6() {
			t.Run(ctxName+"/"+r.path, func(t *testing.T) {
				cfg := opEnv(t)
				cfg.DBPath = filepath.Join(t.TempDir(), "never.db")
				setup(t)
				requireOperatorOnly(t, r.run(t, cfg))
			})
		}
	}
}

// `update --check` only reports: it is open to an agent. In every agent context it succeeds, asks the release
// endpoint once (a GET of the latest-release document) and downloads nothing.
func TestClassCUpdateCheckStaysOpenToAgents(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.1","html_url":"https://example.test/r","assets":[]}`))
	}))
	defer srv.Close()
	was, wasClient := latestReleaseURL, releaseClient
	latestReleaseURL = srv.URL + "/latest"
	releaseClient = func() *release.Client { return &release.Client{} }
	t.Cleanup(func() { latestReleaseURL, releaseClient = was, wasClient })
	for ctxName, setup := range gateOSMenuContexts() {
		t.Run(ctxName, func(t *testing.T) {
			mu.Lock()
			requests = nil
			mu.Unlock()
			cfg := opEnv(t)
			setup(t)
			if _, err := runSendCLI(t, cfg, newUpdateCmd(cfg), "--check"); err != nil {
				t.Fatalf("update --check failed in the %s context: %v", ctxName, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 1 || requests[0] != "GET /latest" {
				t.Fatalf("update --check made requests %v, want one GET /latest", requests)
			}
		})
	}
}

// A command whose operator check is not wired refuses (clicore.OperatorRefusal): none of the verbs of class C
// can be reached by a caller that forgot to hand over the gate.
func TestClassCAnUnwiredGateRefuses(t *testing.T) {
	noop := func(*cobra.Command) {}
	nothing := func(*cobra.Command, string) func(*cobra.Command) (account.Status, bool, error) { return nil }
	cases := map[string]struct {
		cmd  *cobra.Command
		args []string
	}{
		"account logout":   {accountcli.NewCommand(accountcli.Deps{WithJSONErrors: noop, AddSignIn: nothing}), []string{"logout"}},
		"library logout":   {librarycli.NewCommand(librarycli.Deps{WithJSONErrors: noop, AddSignIn: nothing}), []string{"logout"}},
		"library publish":  {librarycli.NewCommand(librarycli.Deps{WithJSONErrors: noop, AddSignIn: nothing}), []string{"publish", "workflow", "x"}},
		"logout":           {logincli.NewLogoutCommand(logincli.Deps{}), []string{"x"}},
		"release keygen":   {releasecli.NewCommand(releasecli.Deps{}), []string{"keygen", "--force", "--stdout-private"}},
		"daemon install":   {daemon.NewCommand(daemon.Deps{}), []string{"install"}},
		"daemon uninstall": {daemon.NewCommand(daemon.Deps{}), []string{"uninstall"}},
		"daemon restart":   {daemon.NewCommand(daemon.Deps{}), []string{"restart"}},
		"update":           {update.NewCommand(update.Deps{}), []string{"--current", "v0.0.1"}},
		"setup":            {doctorcli.NewSetupCommand(doctorcli.Deps{JSONOutput: func() bool { return true }}), []string{"--group", "none"}},
		"doctor fix":       {doctorcli.NewDoctorCommand(doctorcli.Deps{JSONOutput: func() bool { return true }}), []string{"fix", "integrations.mcp_monoagent.register"}},
		"doctor --fix":     {doctorcli.NewDoctorCommand(doctorcli.Deps{JSONOutput: func() bool { return true }}), []string{"--fix", "--group", "none"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			c.cmd.SetOut(&out)
			c.cmd.SetErr(&out)
			c.cmd.SilenceUsage, c.cmd.SilenceErrors = true, true
			c.cmd.SetArgs(c.args)
			if err := c.cmd.Execute(); !isOperatorOnly(err) {
				t.Fatalf("an unwired gate did not refuse: %v", err)
			}
		})
	}
}

// The verbs the owner decided to leave open do not carry the operator-only help text, and the families
// with a flipped verb say so in the guard table. A leave-open verb that is flipped later must change this
// list on purpose.
func TestClassCOpenVerbsStayOpen(t *testing.T) {
	open := []string{
		"db migrate", "init", "httpapi", "mcp", "daemon", "release verify", "account status",
		"account login", "library install", "library update", "integration import-openapi",
	}
	byKey := map[string]string{}
	for _, c := range allCommands(newRootCmd()) {
		byKey[commandKey(c)] = c.Long
	}
	for _, k := range open {
		long, ok := byKey[k]
		if !ok {
			t.Errorf("%s: not a command of the tree", k)
			continue
		}
		if strings.Contains(long, clicore.OperatorOnlyHelp) {
			t.Errorf("%s carries the operator-only help text, but class C left it open (issue 1475)", k)
		}
		for _, r := range operatorOnlyRows() {
			if r.path == k {
				t.Errorf("%s is a row of operatorOnlyRows, but class C left it open (issue 1475)", k)
			}
		}
	}
}
