package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `secret reveal` and `node run vault.secret_get` print a decrypted secret to the operator, and to an agent only
// under the profile's Reveal agent-access level (audited). The agent flavour is whatever opgate refuses: a marked
// agent, the chat or a workflow, a token holder, a process inside a coder turn that cleared its markers, and a
// process that cannot be placed. These tests run the production gate through the real commands.

const (
	revealSecretValue = "turn-gate-secret-value"
	revealWhyTurn     = "this command runs inside a coder session's turn, so a model is running it"
)

type revealGateContext struct {
	name  string
	setup func(t *testing.T, cfg *globalConfig)
	// why is the exact reason the refusal carries, or "" when wantPart is checked
	why      func() string
	wantPart string
}

func revealGateContexts() []revealGateContext {
	return []revealGateContext{
		{name: "marked agent", setup: func(t *testing.T, _ *globalConfig) { t.Setenv("CLAUDECODE", "1") }, why: operator.Refusal},
		{name: "marked chat", setup: func(t *testing.T, _ *globalConfig) { t.Setenv(browserctl.EnvAICaller, browserctl.KindChat) }, why: operator.Refusal},
		{name: "marked workflow", setup: func(t *testing.T, _ *globalConfig) { t.Setenv(workflow.EnvWorkflowRun, "exec-1") }, why: operator.Refusal},
		{name: "in a turn, markers cleared", setup: func(t *testing.T, cfg *globalConfig) {
			needAncestry(t)
			markThisProcessAsACoderTurn(t, cfg)
		}, why: func() string { return revealWhyTurn }},
		{name: "in a turn of a registered host, naming another database", setup: func(t *testing.T, _ *globalConfig) {
			registeredTurnDB(t)
		}, why: func() string { return revealWhyTurn }},
		{name: "token holder", setup: func(t *testing.T, _ *globalConfig) { t.Setenv(coderhost.SessionTokenEnv, "session-token") },
			why: func() string {
				return coderhost.SessionTokenEnv + " is set, so a process holding an agent token is running this command"
			}},
		{name: "database unreadable", setup: func(t *testing.T, cfg *globalConfig) { withoutCoderSessionsColumn(t, cfg.DBPath) },
			wantPart: opgate.UnplacedPrefix},
	}
}

// revealGateFixture is a vault with one entry, the agent-access level set by the operator, and the production
// gate bound to the config.
func revealGateFixture(t *testing.T, level string) *globalConfig {
	t.Helper()
	keyring.MockInit()
	cfg := sendEnv(t)
	cfg.JSONOutput = true
	if _, err := runSendCLI(t, cfg, newSecretCmd(cfg), "add", "--kind", "secret", "--name", "vs", "--value", revealSecretValue); err != nil {
		t.Fatal(err)
	}
	if level != "strict" {
		args := []string{"agent-access", level, "--yes"}
		if _, err := runSendCLI(t, cfg, newSecretCmd(cfg), args...); err != nil {
			t.Fatal(err)
		}
	}
	opgate.SetForTest(t, cfg.gateRefusal)
	return cfg
}

func revealGateAuditRows(t *testing.T, cfg *globalConfig) int {
	t.Helper()
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	rows, err := secrets.AgentAccessAudit(context.Background(), db.DB, "default")
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

type revealVerb struct {
	name string
	what string
	run  func(t *testing.T, cfg *globalConfig) (string, error)
}

func revealVerbs() []revealVerb {
	return []revealVerb{
		{"secret reveal", "secret reveal", func(t *testing.T, cfg *globalConfig) (string, error) {
			return runSendCLI(t, cfg, newSecretCmd(cfg), "reveal", "vs", "--reveal")
		}},
		{"node run vault.secret_get", "node run vault.secret_get", func(t *testing.T, cfg *globalConfig) (string, error) {
			return runSendCLI(t, cfg, newNodeRunCmd(cfg), "vault.secret_get", "--config", `{"name":"vs"}`, "--output", "json")
		}},
	}
}

func checkRevealRefusal(t *testing.T, c revealGateContext, what string, err error) {
	t.Helper()
	if err == nil || clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("%s: not refused (exit 3): %v", c.name, err)
	}
	head := what + " prints decrypted secrets, which only the user reads: "
	tail := "; run it yourself in your terminal or the app"
	if c.wantPart == opgate.UnplacedPrefix { // a process that could not be placed may be the operator
		tail = "; retry, or repair the database"
	}
	msg := err.Error()
	if c.why != nil {
		if want := head + c.why() + tail; msg != want {
			t.Fatalf("%s: refusal text\n got: %s\nwant: %s", c.name, msg, want)
		}
		return
	}
	if !strings.HasPrefix(msg, head) || !strings.Contains(msg, c.wantPart) || !strings.HasSuffix(msg, tail) {
		t.Fatalf("%s: refusal text: %s", c.name, msg)
	}
}

// Below the Reveal level, every agent context is refused with no plaintext and no audit row; in particular a
// process inside a turn that cleared its markers no longer reads through the operator's path.
func TestRevealVerbsRefuseEveryAgentContextBelowTheRevealLevel(t *testing.T) {
	for _, v := range revealVerbs() {
		for _, level := range []string{"strict", "use"} {
			for _, c := range revealGateContexts() {
				t.Run(v.name+"/"+level+"/"+c.name, func(t *testing.T) {
					cfg := revealGateFixture(t, level)
					c.setup(t, cfg)
					out, err := v.run(t, cfg)
					checkRevealRefusal(t, c, v.what, err)
					if strings.Contains(out, revealSecretValue) || strings.Contains(err.Error(), revealSecretValue) {
						t.Fatalf("plaintext leaked: %q %v", out, err)
					}
					if n := revealGateAuditRows(t, cfg); n != 0 {
						t.Fatalf("a refused reveal wrote %d audit row(s)", n)
					}
				})
			}
		}
	}
}

// Under the Reveal level every agent context gets what a marked agent gets: the same bytes, one name-only audit row.
func TestRevealVerbsUnderTheRevealLevelGiveEveryAgentContextTheMarkedAgentsResult(t *testing.T) {
	for _, v := range revealVerbs() {
		golden := ""
		for i, c := range revealGateContexts() {
			t.Run(v.name+"/"+c.name, func(t *testing.T) {
				cfg := revealGateFixture(t, "reveal")
				c.setup(t, cfg)
				out, err := v.run(t, cfg)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 { // the marked agent: the path that did not change
					golden = out
					if !strings.Contains(out, revealSecretValue) {
						t.Fatalf("the marked agent's reveal: %q", out)
					}
				}
				if out != golden {
					t.Fatalf("differs from the marked agent's\n got: %q\nwant: %q", out, golden)
				}
				if n := revealGateAuditRows(t, cfg); n != 1 {
					t.Fatalf("%d audit row(s), want 1", n)
				}
			})
		}
	}
}

// The operator's terminal reveals at any level, unaudited, as before.
func TestRevealVerbsOperatorIsUnchanged(t *testing.T) {
	for _, v := range revealVerbs() {
		t.Run(v.name, func(t *testing.T) {
			cfg := revealGateFixture(t, "strict")
			out, err := v.run(t, cfg)
			if err != nil || !strings.Contains(out, revealSecretValue) {
				t.Fatalf("the operator: %v %q", err, out)
			}
			if n := revealGateAuditRows(t, cfg); n != 0 {
				t.Fatalf("the operator's reveal wrote %d audit row(s)", n)
			}
		})
	}
}

// The chat's and an agent's refusal text is the marker-only text it always was: for a marked caller opgate.Refusal
// and operator.Refusal give the same words.
func TestOpgateRefusalIsTheMarkerTextForAMarkedCaller(t *testing.T) {
	for _, c := range revealGateContexts()[:3] {
		t.Run(c.name, func(t *testing.T) {
			cfg := revealGateFixture(t, "strict")
			c.setup(t, cfg)
			if got, want := opgate.Refusal(), operator.Refusal(); got == "" || got != want {
				t.Fatalf("opgate.Refusal %q, operator.Refusal %q", got, want)
			}
		})
	}
}

// Every combination of two markers (generated from the marker names, not listed by hand) gives the marked caller
// the text operator.Refusal always gave it, in opgate and in the refusal of both reveal verbs: a marker combined
// with the chat's or a workflow run's still reads "the chat" or "a workflow run", not the agent marker.
func TestMarkedCombinationsKeepTheMarkerRefusalText(t *testing.T) {
	names := operator.MarkerEnv()
	if len(names) < 3 {
		t.Fatalf("only %d marker names: %v", len(names), names)
	}
	cfg := revealGateFixture(t, "strict")
	for _, a := range names {
		for _, b := range names {
			t.Run(a+"+"+b, func(t *testing.T) {
				for _, k := range names {
					t.Setenv(k, "")
				}
				t.Setenv(a, "1")
				t.Setenv(b, "1")
				want := operator.Refusal()
				if want == "" {
					t.Skipf("%s + %s is not a marked process", a, b)
				}
				if got := opgate.Refusal(); got != want {
					t.Fatalf("opgate.Refusal %q, operator.Refusal %q", got, want)
				}
				for _, v := range revealVerbs() {
					_, err := v.run(t, cfg)
					if wantMsg := v.what + " prints decrypted secrets, which only the user reads: " + want + "; run it yourself in your terminal or the app"; err == nil || err.Error() != wantMsg {
						t.Fatalf("%s: %v\nwant %s", v.name, err, wantMsg)
					}
				}
			})
		}
	}
}

func revealVerbNamed(v revealVerb, name string) revealVerb {
	switch v.what {
	case "secret reveal":
		v.run = func(t *testing.T, cfg *globalConfig) (string, error) {
			return runSendCLI(t, cfg, newSecretCmd(cfg), "reveal", name, "--reveal")
		}
	default:
		v.run = func(t *testing.T, cfg *globalConfig) (string, error) {
			return runSendCLI(t, cfg, newNodeRunCmd(cfg), "vault.secret_get", "--config", `{"name":"`+name+`"}`, "--output", "json")
		}
	}
	return v
}

// everything a command wrote to stdout and stderr while it ran.
func bothStreams(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	var out string
	func() {
		defer func() { os.Stderr = orig; w.Close() }()
		out = captureStdout(t, fn)
	}()
	return out + string(<-done)
}

// When the audit row of an agent's reveal cannot be written, the value is withheld: the command fails and
// neither stream carries the plaintext, for every agent context and both verbs.
func TestRevealVerbsWithholdThePlaintextWhenTheAuditCannotBeWritten(t *testing.T) {
	for _, v := range revealVerbs() {
		for _, c := range revealGateContexts() {
			t.Run(v.name+"/"+c.name, func(t *testing.T) {
				cfg := revealGateFixture(t, "reveal")
				db, err := storage.NewDatabase(cfg.DBPath)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.DB.Exec(`CREATE TRIGGER audit_broken BEFORE INSERT ON profile_agent_secret_audit BEGIN SELECT RAISE(ABORT, 'audit broken'); END`); err != nil {
					t.Fatal(err)
				}
				db.DB.Close()
				c.setup(t, cfg)
				var out string
				var runErr error
				all := bothStreams(t, func() { out, runErr = v.run(t, cfg) })
				if runErr == nil {
					t.Fatalf("the command succeeded with no audit row: %q", out)
				}
				if strings.Contains(all+out+runErr.Error(), revealSecretValue) {
					t.Fatalf("plaintext reached the output: %q %q %v", all, out, runErr)
				}
				if n := revealGateAuditRows(t, cfg); n != 0 {
					t.Fatalf("%d audit rows", n)
				}
			})
		}
	}
}

// A refused caller causes no lookup of the entry and no keyring access: the gate runs first. The keyring
// fails every call and the name is one the vault does not hold, so a lookup would answer "no secret named" and
// a decrypt would answer the keyring's error; the refusal is all a refused caller sees. The operator control
// shows the same setup does reach the lookup.
func TestRevealVerbsRefuseBeforeAnyLookupOrKeyringAccess(t *testing.T) {
	for _, v := range revealVerbs() {
		for _, level := range []string{"strict", "use"} {
			for _, c := range revealGateContexts() {
				for _, name := range []string{"vs", "missing"} {
					t.Run(v.name+"/"+level+"/"+c.name+"/"+name, func(t *testing.T) {
						cfg := revealGateFixture(t, level)
						keyring.MockInitWithError(errors.New("keyring touched"))
						c.setup(t, cfg)
						out, err := revealVerbNamed(v, name).run(t, cfg)
						checkRevealRefusal(t, c, v.what, err)
						if strings.Contains(out+err.Error(), "keyring touched") || strings.Contains(out+err.Error(), "no secret named") {
							t.Fatalf("the command looked the entry up or touched the keyring: %q %v", out, err)
						}
					})
				}
			}
		}
		t.Run(v.name+"/operator control", func(t *testing.T) {
			cfg := revealGateFixture(t, "strict")
			keyring.MockInitWithError(errors.New("keyring touched"))
			_, err := revealVerbNamed(v, "missing").run(t, cfg)
			if err == nil || !strings.Contains(err.Error(), "no secret named") {
				t.Fatalf("the operator's lookup of a missing name: %v", err)
			}
		})
	}
}

// The gate marks a process it could not place, and only that one: the reveal verbs word that refusal for a
// caller who may be the operator (retry or repair), nothing else.
func TestOnlyAnUnplacedProcessIsMarkedUnplaced(t *testing.T) {
	for _, c := range revealGateContexts() {
		t.Run(c.name, func(t *testing.T) {
			cfg := revealGateFixture(t, "strict")
			c.setup(t, cfg)
			if got, want := opgate.Unplaced(opgate.Refusal()), c.wantPart == opgate.UnplacedPrefix; got != want {
				t.Fatalf("Unplaced(%q) = %v, want %v", opgate.Refusal(), got, want)
			}
		})
	}
}
