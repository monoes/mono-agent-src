package main

import (
	"database/sql"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

// notTheOperator is each kind of caller that is not the operator's own
// terminal: the chat's runs, a process a workflow node started, an agent.
var notTheOperator = []struct{ name, env, value string }{
	{"the chat", browserctl.EnvAICaller, "chat"},
	{"a workflow", workflow.EnvWorkflowRun, "exec-7"},
	{"an agent", "CLAUDECODE", "1"},
}

// configValue reads a configs row: its value, and whether it is there.
func configValue(t *testing.T, dbPath, name string) (string, bool) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	err = db.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, name).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return v, true
}

// The rows that gate approvals, permissions or sandboxing are the
// operator's: config set and delete refuse them to the chat, a workflow and
// an agent (exit 3, operator_only) and leave them as they were. The
// operator still writes them, and any other row is anyone's (#831 F1).
func TestConfigReservedRowsAreTheOperators(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	asOperatorProcess(t)
	cfg := &globalConfig{DBPath: testdb.Path(t), JSONOutput: true}
	run := func(args ...string) error {
		var err error
		captureStdout(t, func() {
			cmd := newConfigCmd(cfg)
			cmd.SetArgs(args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			err = cmd.Execute()
		})
		return err
	}
	reserved := map[string]string{
		"browser_agent_permissions": `{"schema":2,"levels":{"org":"full-no-approvals"}}`,
		"browser_agent_allowlist":   `["https://bank.example.org"]`,
		"coder_mode":                `{"sendApproval":false}`,
		"sandbox.fallback":          "run",
		"automation.autoRepair":     "true",
		"coder_projects":            `[{"origin":"https://bank.example.org","dir":"/"}]`,
	}
	for name := range reserved {
		if err := run("set", name, "operator's"); err != nil {
			t.Fatalf("the operator sets %s: %v", name, err)
		}
	}
	for _, c := range notTheOperator {
		t.Setenv(c.env, c.value)
		for name, loose := range reserved {
			for _, args := range [][]string{{"set", name, loose}, {"set", strings.ToUpper(name), loose}, {"delete", name}} {
				err := run(args...)
				if exitCode(err) != 3 || !strings.Contains(err.Error(), "only the operator can "+args[0]+" config") {
					t.Errorf("%s: config %s: %v", c.name, strings.Join(args, " "), err)
				}
			}
			if v, ok := configValue(t, cfg.DBPath, name); !ok || v != "operator's" {
				t.Errorf("%s changed %s: %q %v", c.name, name, v, ok)
			}
		}
		if err := run("set", "my.key", c.name); err != nil {
			t.Errorf("%s sets an ordinary row: %v", c.name, err)
		}
		if err := run("delete", "my.key"); err != nil {
			t.Errorf("%s deletes an ordinary row: %v", c.name, err)
		}
		t.Setenv(c.env, "")
	}
	if err := run("delete", "coder_mode"); err != nil {
		t.Errorf("the operator deletes coder_mode: %v", err)
	}
}

// The commands made for reserved rows hold too: enabling coder mode and
// setting the sandbox fallback are the operator's (#831 F1).
func TestCoderEnableAndSandboxFallbackAreTheOperators(t *testing.T) {
	dbPath := newSandboxTestDB(t)
	t.Setenv(monomind.EnvOverride, filepath.Join(t.TempDir(), "no-monomind"))
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
	cfg := &globalConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true}
	enable := func() error {
		var err error
		captureStdout(t, func() {
			cmd := newCoderCmd(cfg)
			cmd.SetArgs([]string{"enable", "--yes-i-understand"})
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			err = cmd.Execute()
		})
		return err
	}
	for _, c := range notTheOperator {
		t.Setenv(c.env, c.value)
		if err := enable(); exitCode(err) != 3 || !strings.Contains(err.Error(), "only the operator can enable coder mode") {
			t.Errorf("%s enables coder mode: %v", c.name, err)
		}
		if _, code, out := runSandboxCLI(t, dbPath, "--set", "run"); code != 3 || !strings.Contains(out, "operator_only") {
			t.Errorf("%s sets the sandbox fallback: exit %d %s", c.name, code, out)
		}
		t.Setenv(c.env, "")
	}
	if v, ok := configValue(t, dbPath, "coder_mode"); ok && strings.Contains(v, `"enabled":true`) {
		t.Errorf("coder mode was enabled: %s", v)
	}
	if _, ok := configValue(t, dbPath, "sandbox.fallback"); ok {
		t.Error("the sandbox fallback was set")
	}
	if err := enable(); err != nil {
		t.Errorf("the operator enables coder mode: %v", err)
	}
	if rep, code, out := runSandboxCLI(t, dbPath, "--set", "strictest"); code != 0 || rep.Value != "strictest" {
		t.Errorf("the operator sets the sandbox fallback: exit %d %s", code, out)
	}
}

// Where a full-access chat works and how far it may go are the operator's:
// coder set's limits and the site-to-folder map refuse the chat, a workflow
// and an agent, and change nothing (#831). So do the dynamic org's router,
// model picker and writers: every coder set flag is the operator's now
// (TestEveryCoderSetFlagIsTheOperators).
func TestCoderLimitsAndProjectsAreTheOperators(t *testing.T) {
	dbPath := newSandboxTestDB(t)
	t.Setenv(monomind.EnvOverride, filepath.Join(t.TempDir(), "no-monomind"))
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
	cfg := &globalConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true}
	coder := func(args ...string) error {
		var err error
		captureStdout(t, func() {
			cmd := newCoderCmd(cfg)
			cmd.SetArgs(args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			err = cmd.Execute()
		})
		return err
	}
	project := t.TempDir()
	limits := [][]string{
		{"set", "--budget-usd", "500"}, {"set", "--max-turns", "900"}, {"set", "--timeout", "100h"},
		{"set", "--workspace-root", project}, {"set", "--org-max-agents", "20"}, {"set", "--org-max-concurrent", "10"},
		{"set", "--org-budget-usd", "500"}, {"set", "--org-router", "jev"}, {"set", "--org-model-picker", "lead"},
		{"set", "--org-writers", "isolated"},
	}
	for _, c := range notTheOperator {
		t.Setenv(c.env, c.value)
		for _, args := range append(limits, []string{"projects", "map", "https://bank.example.org", project}, []string{"projects", "unmap", "https://bank.example.org"}) {
			if err := coder(args...); exitCode(err) != 3 {
				t.Errorf("%s: coder %s: %v", c.name, strings.Join(args, " "), err)
			}
		}
		t.Setenv(c.env, "")
	}
	if v, ok := configValue(t, dbPath, "coder_mode"); ok && (strings.Contains(v, "500") || strings.Contains(v, project) ||
		strings.Contains(v, `"orgRouter":"jev"`) || strings.Contains(v, `"orgWriters":"isolated"`) || strings.Contains(v, `"orgModelPicker":"lead"`)) {
		t.Errorf("a limit changed: %s", v)
	}
	if v, ok := configValue(t, dbPath, "coder_projects"); ok && strings.Contains(v, "bank.example.org") {
		t.Errorf("the map changed: %s", v)
	}
	for _, args := range append(limits, []string{"projects", "map", "https://bank.example.org", project}) {
		if err := coder(args...); err != nil {
			t.Errorf("the operator: coder %s: %v", strings.Join(args, " "), err)
		}
	}
}
