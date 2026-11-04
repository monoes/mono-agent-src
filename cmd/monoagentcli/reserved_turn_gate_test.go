package main

import (
	"context"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/storage"
)

// The HTTP API's bearer (the vault entry httpapi-token) is written by the user's own terminal only. These tests
// run the production gate through the real `secret` commands: every context opgate refuses (a marked agent,
// the chat, a workflow, a process inside a coder turn that cleared its markers, one in a turn of a registered
// host naming another database, a token holder, one that cannot be placed) writes, replaces and deletes
// nothing, and the entry stays byte for byte what the user set.

const (
	reservedBearer = "bearer-the-user-set"
	reservedPick   = "bearer-a-model-would-pick"
)

func reservedGateFixture(t *testing.T, withEntry bool) *globalConfig {
	t.Helper()
	keyring.MockInit()
	cfg := sendEnv(t)
	if withEntry {
		if _, err := runSendCLI(t, cfg, newSecretCmd(cfg), "add", "--kind", "secret", "--name", secrets.ReservedEntry, "--field", "token="+reservedBearer); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runSendCLI(t, cfg, newSecretCmd(cfg), "add", "--kind", "secret", "--name", "other", "--value", "x"); err != nil {
		t.Fatal(err)
	}
	opgate.SetForTest(t, cfg.gateRefusal)
	return cfg
}

// reservedBearerState is the number of entries named httpapi-token and the bearer of the last one.
func reservedBearerState(t *testing.T, cfg *globalConfig) (int, string) {
	t.Helper()
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	entries, err := secrets.List(context.Background(), db.DB, "default")
	if err != nil {
		t.Fatal(err)
	}
	n, bearer := 0, ""
	for _, e := range entries {
		if e.Name != secrets.ReservedEntry {
			continue
		}
		n++
		fields, _, err := secrets.DecryptFields(context.Background(), db.DB, "default", e.ID)
		if err != nil {
			t.Fatal(err)
		}
		bearer = fields[secrets.ReservedTokenField]
	}
	return n, bearer
}

func checkReservedGateRefusal(t *testing.T, c revealGateContext, what string, err error) {
	t.Helper()
	if err == nil || clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("%s: not refused (exit 3): %v", c.name, err)
	}
	head := what + ": " + secrets.ErrReservedEntry.Error() + ": "
	tail := "; run it yourself in your terminal or the app (its bearer is the field token: --field token=<value>)"
	if c.wantPart == opgate.UnplacedPrefix {
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

func TestReservedBearerIsNotWrittenOrDeletedByAnyNonOperatorContext(t *testing.T) {
	verbs := []struct {
		name, what string
		args       []string
	}{
		{"update", "updating entry", []string{"update", secrets.ReservedEntry, "--field", "token=" + reservedPick}},
		{"update --merge", "updating entry", []string{"update", secrets.ReservedEntry, "--merge", "--field", "token=" + reservedPick}},
		{"rename onto it", "updating entry", []string{"update", "other", "--name", secrets.ReservedEntry}},
		{"rm", "deleting entry", []string{"rm", secrets.ReservedEntry}},
	}
	for _, v := range verbs {
		for _, c := range revealGateContexts() {
			t.Run(v.name+"/"+c.name, func(t *testing.T) {
				cfg := reservedGateFixture(t, true)
				c.setup(t, cfg)
				_, err := runSendCLI(t, cfg, newSecretCmd(cfg), v.args...)
				checkReservedGateRefusal(t, c, v.what, err)
				if n, got := reservedBearerState(t, cfg); n != 1 || got != reservedBearer {
					t.Fatalf("%s: the reserved entry changed: %d entries, bearer %q", c.name, n, got)
				}
			})
		}
	}
	for _, c := range revealGateContexts() {
		t.Run("add/"+c.name, func(t *testing.T) {
			cfg := reservedGateFixture(t, false)
			c.setup(t, cfg)
			_, err := runSendCLI(t, cfg, newSecretCmd(cfg), "add", "--kind", "secret", "--name", secrets.ReservedEntry, "--field", "token="+reservedPick)
			checkReservedGateRefusal(t, c, "adding secret", err)
			if n, _ := reservedBearerState(t, cfg); n != 0 {
				t.Fatalf("%s: a refused add wrote %d reserved entries", c.name, n)
			}
		})
	}
}

func TestReservedBearerIsTheOperatorsToWrite(t *testing.T) {
	cfg := reservedGateFixture(t, true)
	if _, err := runSendCLI(t, cfg, newSecretCmd(cfg), "update", secrets.ReservedEntry, "--field", "token="+reservedPick); err != nil {
		t.Fatalf("the operator's update: %v", err)
	}
	if n, got := reservedBearerState(t, cfg); n != 1 || got != reservedPick {
		t.Fatalf("after the operator's update: %d entries, bearer %q", n, got)
	}
	if _, err := runSendCLI(t, cfg, newSecretCmd(cfg), "rm", secrets.ReservedEntry); err != nil {
		t.Fatalf("the operator's rm: %v", err)
	}
	if n, _ := reservedBearerState(t, cfg); n != 0 {
		t.Fatalf("the operator's rm left %d entries", n)
	}
}

// operator.NotUser is the verdict of opgate.Refusal (non-recovery) for packages that cannot import opgate:
// the two are empty together in every context, so they cannot drift; for a marked process the text is the
// one operator.Refusal has always given, whatever else it holds.
func TestNotUserAgreesWithTheGateInEveryContext(t *testing.T) {
	contexts := append([]revealGateContext{{name: "operator", setup: func(*testing.T, *globalConfig) {}}}, revealGateContexts()...)
	for _, c := range contexts {
		t.Run(c.name, func(t *testing.T) {
			cfg := reservedGateFixture(t, false)
			c.setup(t, cfg)
			gate, notUser := opgate.Refusal(), operator.NotUser()
			if (gate == "") != (notUser == "") {
				t.Fatalf("drift: opgate.Refusal() = %q, operator.NotUser() = %q", gate, notUser)
			}
			if c.name == "operator" && gate != "" {
				t.Fatalf("the operator is refused: %s", gate)
			}
			if c.name != "operator" && notUser == "" {
				t.Fatal("a refused context is let through")
			}
			if operator.Marker() != "" && notUser != operator.Refusal() {
				t.Fatalf("a marked caller's text changed: %q", notUser)
			}
		})
	}
	t.Run("no turn check registered: the marker alone", func(t *testing.T) {
		needAncestry(t)
		cfg := reservedGateFixture(t, false)
		markThisProcessAsACoderTurn(t, cfg)
		opgate.SetForTest(t, nil)
		if got := operator.NotUser(); got != "" {
			t.Fatalf("a process that registered no turn check is judged by its marker alone, got %q", got)
		}
		t.Setenv("CLAUDECODE", "1")
		if got := operator.NotUser(); got != operator.Refusal() || got == "" {
			t.Fatalf("marked, no turn check: %q", got)
		}
	})
}
