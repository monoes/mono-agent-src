package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	taskcli "github.com/monoes/mono-agent/internal/cli/task"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// reqOpRow is one wiring point of requireOperator (the func the adapters hand their Deps) or its
// direct caller. file is the source file of the wiring (the guard compares them with the sources);
// build and args reach a verb that asks the gate. A row with direct set has no simple CLI verb:
// the func the adapter wires is called itself.
type reqOpRow struct {
	name   string
	file   string
	build  func(*globalConfig) *cobra.Command
	args   []string
	direct func(*globalConfig) func(string) error
}

func underRoot(use string, build func(*globalConfig) []*cobra.Command) func(*globalConfig) *cobra.Command {
	return func(cfg *globalConfig) *cobra.Command {
		c := &cobra.Command{Use: use}
		c.AddCommand(build(cfg)...)
		return c
	}
}

func requireOperatorRows() []reqOpRow {
	return []reqOpRow{
		{name: "aitask submit", file: "aitask_adapter.go", build: newAitaskCmd, args: []string{"submit"}},
		{name: "application fill start", file: "application_fill_wire.go", build: newApplicationCmd, args: []string{"fill", "start", "no-such-application"}},
		{name: "chat coder set --send-approval", file: "chat_adapter.go", build: newCoderSetCmd, args: []string{"--send-approval=true"}},
		{name: "config set (reserved row)", file: "config_list_template_adapter.go", build: newConfigCmd, args: []string{"set", "agent_purposes.x", "v"}},
		{name: "doctor (workflow publication hooks)", file: "doctor_adapter.go", direct: func(cfg *globalConfig) func(string) error { return healthEnvDeps(cfg).RequireOperator }},
		{name: "hil allow add", file: "hil.go", build: newHILCmd, args: []string{"allow", "add"}},
		{name: "image scan on", file: "image.go", build: newImageCmd, args: []string{"scan", "on"}},
		{name: "workflow apply-fix", file: "workflow_aitask.go", build: underRoot("workflow", newWorkflowAitaskCmds), args: []string{"apply-fix", "no-such-task"}},
		{name: "publication delete", file: "publication.go", build: newPublicationCmd, args: []string{"delete", "no-such-publication"}},
		{name: "sandbox fallback --set", file: "sandbox.go", build: newSandboxCmd, args: []string{"fallback", "--set", "strictest"}},
	}
}

func (r reqOpRow) run(t *testing.T, cfg *globalConfig) error {
	t.Helper()
	if r.direct != nil {
		return r.direct(cfg)("do the thing")
	}
	_, err := runSendCLI(t, cfg, r.build(cfg), r.args...)
	return err
}

// The guard: every place cmd/monoagentcli hands requireOperator to a Deps, or calls it, has a row.
var reqOpWiring = regexp.MustCompile(`(?:RequireOperator|Operator)\s*(?::|=)\s*\w+\.requireOperator\b|\b\w+\.requireOperator\(`)

func TestEveryRequireOperatorWiringHasARowInTheTable(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no sources: %v", err)
	}
	inSource := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := len(reqOpWiring.FindAll(b, -1)); n > 0 {
			inSource[f] = n
		}
	}
	if len(inSource) == 0 {
		t.Fatal("found no wiring: the guard's pattern is stale")
	}
	inTable := map[string]int{}
	for _, r := range requireOperatorRows() {
		inTable[r.file]++
	}
	var bad []string
	for f := range inSource {
		if inTable[f] == 0 {
			bad = append(bad, f+": wired in the source, no row")
		}
	}
	for f := range inTable {
		if inSource[f] == 0 {
			bad = append(bad, f+": row, but no wiring in the source")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("requireOperatorRows and the wiring disagree: %q", bad)
	}
}

func isOperatorOnly(err error) bool {
	var coded interface{ JSONErrorFields() map[string]any }
	return err != nil && errors.As(err, &coded) && coded.JSONErrorFields()["code"] == "operator_only"
}

// (a) inside a coder turn, with a clean operator environment and no token: refused with the turn wording.
func TestRequireOperatorRefusesAProcessInsideACoderTurn(t *testing.T) {
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
	for _, r := range requireOperatorRows() {
		t.Run(r.name, func(t *testing.T) {
			cfg := sendEnv(t)
			markThisProcessAsACoderTurn(t, cfg)
			err := r.run(t, cfg)
			requireOperatorOnly(t, err)
			for _, want := range []string{"coder session's turn", "only the operator can ", "run it in your own terminal"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("not refused by the turn ancestry (missing %q): %v", want, err)
				}
			}
		})
	}
}

// (b) the environment contexts: agent, workflow, chat, mission token only, session token only.
func TestRequireOperatorRefusesTheEnvironmentContexts(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		for _, r := range requireOperatorRows() {
			t.Run(ctxName+"/"+r.name, func(t *testing.T) {
				cfg := sendEnv(t)
				setup(t)
				requireOperatorOnly(t, r.run(t, cfg))
			})
		}
	}
}

// (c) the operator passes the gate: whatever the verb answers next, it is not an operator_only refusal.
func TestRequireOperatorLetsTheOperatorThrough(t *testing.T) {
	for _, r := range requireOperatorRows() {
		t.Run(r.name, func(t *testing.T) {
			cfg := sendEnv(t)
			if err := r.run(t, cfg); isOperatorOnly(err) {
				t.Fatalf("the operator was refused: %v", err)
			}
		})
	}
}

// (d) a fresh machine: no database file. The operator passes every verb's gate, and the gate itself
// creates no file (the verbs open the database after it, so only the gate is checked for that).
func TestRequireOperatorWithNoDatabaseLetsTheOperatorThroughAndCreatesNothing(t *testing.T) {
	t.Run("the gate itself", func(t *testing.T) {
		cfg := sendEnv(t)
		cfg.DBPath = filepath.Join(t.TempDir(), "sub", "never.db")
		if err := cfg.requireOperator("do the thing"); err != nil {
			t.Fatalf("the operator on a fresh machine was refused: %v", err)
		}
		if _, err := os.Stat(filepath.Dir(cfg.DBPath)); err == nil {
			t.Fatal("the gate created the database directory")
		}
	})
	for _, r := range requireOperatorRows() {
		t.Run(r.name, func(t *testing.T) {
			cfg := sendEnv(t)
			cfg.DBPath = filepath.Join(t.TempDir(), "fresh.db")
			if err := r.run(t, cfg); isOperatorOnly(err) {
				t.Fatalf("the operator on a fresh machine was refused: %v", err)
			}
		})
	}
	t.Run("a database without the coder sessions table", func(t *testing.T) {
		cfg := sendEnv(t)
		cfg.DBPath = filepath.Join(t.TempDir(), "bare.db")
		d, err := storage.NewDatabase(cfg.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		d.Close()
		if err := cfg.requireOperator("do the thing"); err != nil {
			t.Fatalf("no turn was ever recorded, yet: %v", err)
		}
	})
}

// (e) a coder_sessions that cannot be queried refuses (fails closed), whatever the verb: the table
// is there (migrated), without the column the ancestry check reads.
func TestRequireOperatorFailsClosedWhenTheTurnsCannotBeRead(t *testing.T) {
	for _, r := range requireOperatorRows() {
		t.Run(r.name, func(t *testing.T) {
			cfg := sendEnv(t)
			d, err := storage.NewDatabase(cfg.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := d.DB.Exec(`ALTER TABLE coder_sessions RENAME COLUMN turn_pid TO turn_pid_gone`); err != nil {
				t.Fatal(err)
			}
			d.Close()
			err = r.run(t, cfg)
			if err == nil || !strings.Contains(err.Error(), "turn_pid") {
				t.Fatalf("a query error did not refuse: %v", err)
			}
		})
	}
}

func TestRequireOperatorFailsClosedOnAnUnreadableDatabaseFile(t *testing.T) {
	cfg := sendEnv(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(cfg.DBPath, []byte("this is not a database, and it is long enough to be read as a header, or not"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cfg.requireOperator("do the thing"); err == nil {
		t.Fatal("an unreadable database passed the gate")
	}
}

// The token variables are read first, before the operator check and before any database: every
// variable of the shared list refuses (even a value the operator check would not count as a marker),
// with the token wording; the gate itself opens no database for it.
func TestRequireOperatorRefusesEachAgentTokenVariableFirst(t *testing.T) {
	for _, key := range taskcli.PersonTokenEnv() {
		for _, r := range requireOperatorRows() {
			t.Run(key+"/"+r.name, func(t *testing.T) {
				cfg := sendEnv(t)
				t.Setenv(key, " x ")
				err := r.run(t, cfg)
				requireOperatorOnly(t, err)
				for _, want := range []string{key + " is set", "holding an agent token", "only the operator can "} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("not refused by the token layer (missing %q): %v", want, err)
					}
				}
			})
		}
		t.Run(key+"/the gate opens no database", func(t *testing.T) {
			cfg := sendEnv(t)
			cfg.DBPath = filepath.Join(t.TempDir(), "never-opened.db")
			t.Setenv(key, " x ")
			requireOperatorOnly(t, cfg.requireOperator("do the thing"))
			if _, statErr := os.Stat(cfg.DBPath); statErr == nil {
				t.Error("the database was opened by a refused token holder")
			}
		})
	}
}
