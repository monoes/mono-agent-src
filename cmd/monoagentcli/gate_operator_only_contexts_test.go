package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	taskcli "github.com/monoes/mono-agent/internal/cli/task"
	"github.com/monoes/mono-agent/internal/cli/workflowrun"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// opEnv is sendEnv with the production gate bound to its config: the converted verbs ask opgate, which in
// a test binary has no body until a test registers one (the real tree registers it at process start).
func opEnv(t *testing.T) *globalConfig {
	t.Helper()
	cfg := sendEnv(t)
	opgate.SetForTest(t, cfg.gateRefusal)
	t.Cleanup(func() { workflowrun.FullOutputs = false })
	return cfg
}

func (r opRow) run(t *testing.T, cfg *globalConfig) error {
	t.Helper()
	_, err := runSendCLI(t, cfg, r.build(cfg), r.args...)
	return err
}

func needAncestry(t *testing.T) {
	t.Helper()
	if coderhost.ProcStamp(os.Getpid()) == "" {
		t.Skip("process ancestry cannot be read here")
	}
}

func withoutCoderSessionsColumn(t *testing.T, path string) {
	t.Helper()
	d, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.DB.Exec(`ALTER TABLE coder_sessions RENAME COLUMN turn_pid TO turn_pid_gone`); err != nil {
		t.Fatal(err)
	}
}

// (a) inside a coder turn (this process is recorded as the turn), clean environment, no token: refused
// with the turn wording. Recovery verbs too: the database is readable and holds the turn.
func TestOperatorOnlyVerbsRefuseAProcessInsideACoderTurn(t *testing.T) {
	needAncestry(t)
	for _, r := range operatorOnlyRows() {
		t.Run(r.path, func(t *testing.T) {
			cfg := opEnv(t)
			markThisProcessAsACoderTurn(t, cfg)
			err := r.run(t, cfg)
			requireOperatorOnly(t, err)
			if !strings.Contains(err.Error(), "coder session's turn") {
				t.Fatalf("not refused by the turn ancestry: %v", err)
			}
		})
	}
}

// (b) the five environment contexts: agent, workflow, chat, mission token only, session token only.
func TestOperatorOnlyVerbsRefuseTheEnvironmentContexts(t *testing.T) {
	for ctxName, setup := range gateOSMenuContexts() {
		for _, r := range operatorOnlyRows() {
			t.Run(ctxName+"/"+r.path, func(t *testing.T) {
				cfg := opEnv(t)
				setup(t)
				requireOperatorOnly(t, r.run(t, cfg))
			})
		}
	}
}

// A token holder is refused before any database is read: the file is never created.
func TestOperatorOnlyVerbsRefuseATokenHolderWithoutTouchingTheDatabase(t *testing.T) {
	for _, ctxName := range []string{"mission-token", "session-token"} {
		for _, r := range operatorOnlyRows() {
			t.Run(ctxName+"/"+r.path, func(t *testing.T) {
				cfg := opEnv(t)
				cfg.DBPath = filepath.Join(t.TempDir(), "never.db")
				gateOSMenuContexts()[ctxName](t)
				err := r.run(t, cfg)
				requireOperatorOnly(t, err)
				if !strings.Contains(err.Error(), "agent token") {
					t.Fatalf("not refused by the token layer: %v", err)
				}
				if _, statErr := os.Stat(cfg.DBPath); statErr == nil {
					t.Fatal("the database was created for a refused token holder")
				}
			})
		}
	}
}

// (c) the operator passes the gate: whatever the verb answers next, it is not an operator_only refusal.
func TestOperatorOnlyVerbsLetTheOperatorThrough(t *testing.T) {
	for _, r := range operatorOnlyRows() {
		t.Run(r.path, func(t *testing.T) {
			cfg := opEnv(t)
			if err := r.run(t, cfg); isOperatorOnly(err) {
				t.Fatalf("the operator was refused: %v", err)
			}
		})
	}
}

// (d) a fresh machine: no database. The operator passes, and the gate itself creates no file.
func TestOperatorOnlyGateWithNoDatabaseLetsTheOperatorThroughAndCreatesNothing(t *testing.T) {
	cfg := opEnv(t)
	cfg.DBPath = filepath.Join(t.TempDir(), "sub", "never.db")
	for _, recovery := range []bool{false, true} {
		if why := opgateAnswer(recovery); why != "" {
			t.Fatalf("recovery=%v: the operator on a fresh machine was refused: %s", recovery, why)
		}
	}
	if _, err := os.Stat(filepath.Dir(cfg.DBPath)); err == nil {
		t.Fatal("the gate created the database directory")
	}
	for _, r := range operatorOnlyRows() {
		t.Run(r.path, func(t *testing.T) {
			cfg := opEnv(t)
			cfg.DBPath = filepath.Join(t.TempDir(), "fresh.db")
			if err := r.run(t, cfg); isOperatorOnly(err) {
				t.Fatalf("the operator on a fresh machine was refused: %v", err)
			}
		})
	}
}

func opgateAnswer(recovery bool) string {
	if recovery {
		return opgate.RecoveryRefusal()
	}
	return opgate.Refusal()
}

// (e) damaged turn records. A readable database whose coder_sessions cannot be queried, and a file that is
// not a database, refuse every verb that is not a recovery verb (a process that cannot be placed is not let
// through); a recovery verb lets the operator through, because the repair is what the damage needs.
func TestOperatorOnlyVerbsOnDamagedTurnRecords(t *testing.T) {
	damage := map[string]func(*testing.T, *globalConfig){
		"a coder_sessions without turn_pid": func(t *testing.T, cfg *globalConfig) { withoutCoderSessionsColumn(t, cfg.DBPath) },
		"a file that is not a database": func(t *testing.T, cfg *globalConfig) {
			cfg.DBPath = filepath.Join(t.TempDir(), "junk.db")
			if err := os.WriteFile(cfg.DBPath, []byte("this is not a database, and it is long enough to be read as a header, or not"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, hurt := range damage {
		for _, r := range operatorOnlyRows() {
			t.Run(name+"/"+r.path, func(t *testing.T) {
				cfg := opEnv(t)
				hurt(t, cfg)
				err := r.run(t, cfg)
				if r.recovery {
					if isOperatorOnly(err) {
						t.Fatalf("a recovery verb locked the operator out of a damaged database: %v", err)
					}
					return
				}
				requireOperatorOnly(t, err)
				if !strings.Contains(err.Error(), "cannot tell whether a coder session's turn runs this command") {
					t.Fatalf("refused, but not as a process that cannot be placed: %v", err)
				}
			})
		}
	}
}

// A recovery verb still refuses a marked turn on a readable database, and the gate reads the database
// without writing it.
func TestRecoveryVerbsStillRefuseARecordedTurn(t *testing.T) {
	needAncestry(t)
	cfg := opEnv(t)
	markThisProcessAsACoderTurn(t, cfg)
	if opgate.RecoveryRefusal() == "" {
		t.Fatal("the recovery gate let a process inside a recorded turn through")
	}
}

// (f) the registry: a turn recorded in a registered host database X is found whatever database the command
// is pointed at (--db-path on an empty file, a missing path, a second database).
func TestOperatorOnlyVerbsRefuseATurnOfARegisteredHostWhateverDatabaseTheyName(t *testing.T) {
	for name := range overrides(t) {
		for _, r := range operatorOnlyRows() {
			t.Run(name+"/"+r.path, func(t *testing.T) {
				cfg := opEnv(t)
				registeredTurnDB(t)
				overrides(t)[name](cfg)
				err := r.run(t, cfg)
				requireOperatorOnly(t, err)
				if !strings.Contains(err.Error(), "coder session's turn") {
					t.Fatalf("not refused by the turn layer: %v", err)
				}
			})
		}
	}
}

// The registration the real command tree does at start-up binds the gate to its config.
func TestRegisterOperatorGateBindsTheGateToTheConfig(t *testing.T) {
	asOperatorProcess(t)
	opgate.SetForTest(t, nil)
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "x.db")}
	defer func() {
		if recover() != nil {
			t.Fatal("registerOperatorGate was refused by the guard")
		}
	}()
	registerOperatorGate(cfg)
	t.Setenv(taskcli.PersonTokenEnv()[0], "x")
	if opgate.Refusal() == "" {
		t.Fatal("after registration a token holder is not refused")
	}
}

// The wiring: the real tree registers the gate (not under test binaries, which register their own).
func TestTheRealCommandTreeRegistersTheOperatorGate(t *testing.T) {
	b, err := os.ReadFile("root.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "registerOperatorGate(cfg)") {
		t.Fatal("root.go does not register the operator gate")
	}
}
