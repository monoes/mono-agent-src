package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `publication register` by anyone but the user's own terminal needs an approved send (requirePublicationApproval).
// That decision asks opgate.Refusal: a MARKED caller keeps the text and the claim behaviour it always had, and a
// process inside a coder turn (or holding an agent token) that cleared its markers is handled like it.

const (
	pubTurnWhy       = "this command runs inside a coder session's turn, so a model is running it"
	pubUnreadableWhy = "cannot tell whether a coder session's turn runs this command ("
	pubEntry         = `{"platform":"email","kind":"post","body":"hello","remote_id":"1","published_at":"2026-10-13T10:00:00Z"}`
)

type pubGateContext struct {
	name     string
	setup    func(t *testing.T, cfg *globalConfig)
	want     func() string // the exact why, or
	wantPart string        // a part of it
	// approvedWorks: after the user approved a send, this context registers with --approval like a marked agent
	approvedWorks bool
}

func pubGateContexts() []pubGateContext {
	return []pubGateContext{
		{name: "marked agent", setup: func(t *testing.T, _ *globalConfig) { asAgentProcess(t) }, want: operator.Refusal, approvedWorks: true},
		{name: "marked chat", setup: func(t *testing.T, _ *globalConfig) {
			asOperatorProcess(t)
			t.Setenv(browserctl.EnvAICaller, browserctl.KindChat)
		}, want: operator.Refusal, approvedWorks: true},
		{name: "marked workflow", setup: func(t *testing.T, _ *globalConfig) {
			asOperatorProcess(t)
			t.Setenv(workflow.EnvWorkflowRun, "exec-1")
		}, want: operator.Refusal, approvedWorks: true},
		{name: "in a turn, markers cleared", setup: func(t *testing.T, cfg *globalConfig) {
			needAncestry(t)
			markThisProcessAsACoderTurn(t, cfg)
		}, want: func() string { return pubTurnWhy }, approvedWorks: true},
		{name: "in a turn of a registered host, naming another database", setup: func(t *testing.T, _ *globalConfig) {
			registeredTurnDB(t) // the fixture's database holds no turn: the registry finds it
		}, want: func() string { return pubTurnWhy }, approvedWorks: true},
		{name: "token holder", setup: func(t *testing.T, _ *globalConfig) {
			t.Setenv(coderhost.SessionTokenEnv, "session-token")
		}, want: func() string {
			return coderhost.SessionTokenEnv + " is set, so a process holding an agent token is running this command"
		}, approvedWorks: true},
		{name: "database unreadable", setup: func(t *testing.T, cfg *globalConfig) {
			withoutCoderSessionsColumn(t, cfg.DBPath)
		}, wantPart: pubUnreadableWhy},
	}
}

func registerPublication(t *testing.T, cfg *globalConfig, args ...string) error {
	t.Helper()
	cmd := newPublicationCmd(cfg)
	cmd.SetIn(strings.NewReader(pubEntry))
	_, err := runSendCLI(t, cfg, cmd, append([]string{"register", "--stdin-json"}, args...)...)
	return err
}

func registeredCount(t *testing.T, cfg *globalConfig) int {
	t.Helper()
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, err := publication.NewStore(db.DB, "default").List(t.Context(), publication.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func (c pubGateContext) check(t *testing.T, err error) {
	t.Helper()
	if err == nil || exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "registering a publication from an agent needs an approved send") {
		t.Fatalf("%s: not refused as operator_only (exit 3): %v", c.name, err)
	}
	if c.want != nil {
		if suffix := "Asked because " + c.want(); !strings.HasSuffix(err.Error(), suffix) {
			t.Fatalf("%s: refusal does not end with %q: %s", c.name, suffix, err.Error())
		}
		return
	}
	if !strings.Contains(err.Error(), "Asked because "+c.wantPart) {
		t.Fatalf("%s: refusal text: %s", c.name, err.Error())
	}
}

func TestPublicationRegisterNeedsAnApprovedSendInEveryContextThatIsNotTheUser(t *testing.T) {
	for _, ctx := range pubGateContexts() {
		t.Run(ctx.name, func(t *testing.T) {
			cfg := sendEnv(t)
			opgate.SetForTest(t, cfg.gateRefusal)
			ctx.setup(t, cfg)
			ctx.check(t, registerPublication(t, cfg))
			ctx.check(t, registerPublication(t, cfg, "--approval", "no-such-send"))
			if n := registeredCount(t, cfg); n != 0 {
				t.Fatalf("a refused register stored %d publications", n)
			}
		})
	}
	t.Run("operator", func(t *testing.T) {
		cfg := sendEnv(t)
		opgate.SetForTest(t, cfg.gateRefusal)
		if err := registerPublication(t, cfg); err != nil {
			t.Fatalf("operator register: %v", err)
		}
		if n := registeredCount(t, cfg); n != 1 {
			t.Fatalf("%d publications, want 1", n)
		}
	})
}

// With a send the user approved and that was sent, a process in any of these contexts registers its publication
// as a marked agent does; the approval is spent on it, so another publication with it is refused.
func TestPublicationRegisterWithAnApprovedSendWorksInEveryAgentContext(t *testing.T) {
	for _, ctx := range pubGateContexts() {
		if !ctx.approvedWorks {
			continue
		}
		t.Run(ctx.name, func(t *testing.T) {
			cfg := sendEnv(t)
			swapRunner(t)
			id := queueViaNodeRun(t, cfg, sendConfig)
			asOperatorProcess(t)
			if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "resolve", id, "--approve"); err != nil {
				t.Fatal(err)
			}
			asOperatorProcess(t)
			opgate.SetForTest(t, cfg.gateRefusal)
			ctx.setup(t, cfg)
			if err := registerPublication(t, cfg, "--approval", id); err != nil {
				t.Fatalf("register with a sent approval: %v", err)
			}
			if n := registeredCount(t, cfg); n != 1 {
				t.Fatalf("%d publications, want 1", n)
			}
			// the claim is spent on that publication
			other := strings.NewReplacer(`"remote_id":"1"`, `"remote_id":"2"`, "hello", "hello again")
			cmd := newPublicationCmd(cfg)
			cmd.SetIn(strings.NewReader(other.Replace(pubEntry)))
			if _, err := runSendCLI(t, cfg, cmd, "register", "--stdin-json", "--approval", id); err == nil || !strings.Contains(err.Error(), "already registered a publication") {
				t.Fatalf("another publication with the spent approval: %v", err)
			}
			if n := registeredCount(t, cfg); n != 1 {
				t.Fatalf("%d publications after the refused one, want 1", n)
			}
		})
	}
}

// The wiring: the Refusal publication register asks is the turn-aware gate, not the marker check.
func TestPublicationRegisterAsksTheTurnAwareGate(t *testing.T) {
	cfg := sendEnv(t)
	opgate.SetForTest(t, func(bool) string { return "in a coder turn" })
	err := registerPublication(t, cfg)
	if err == nil || !strings.HasSuffix(err.Error(), "Asked because in a coder turn") {
		t.Fatalf("register behind a gate that refuses: %v", err)
	}
	if n := registeredCount(t, cfg); n != 0 {
		t.Fatalf("%d publications registered", n)
	}
}

// The marked path is byte-identical: every pair of marker variables with every MONOAGENT_ACTOR value gets the very
// text operator.Refusal gave before register asked opgate. Needs the combined-marker fix of the turn-aware gate
// (opgate.Refusal must keep operator.Refusal's text for a marked caller that also carries MONOAGENT_AI_CALLER or
// MONOAGENT_WORKFLOW_RUN); until it is merged this test fails on those pairs.
func TestMarkedCallersGetTheSamePublicationRefusalWhateverCombinationOfMarkersTheyCarry(t *testing.T) {
	cfg := sendEnv(t)
	opgate.SetForTest(t, cfg.gateRefusal)
	names := operator.MarkerEnv()
	actors := []string{"", "claude", "operator"}
	cases := 0
	for i, a := range names {
		for _, b := range names[i:] {
			for _, actor := range actors {
				asOperatorProcess(t)
				t.Setenv(a, "x")
				t.Setenv(b, "x")
				t.Setenv("MONOAGENT_ACTOR", actor)
				want := operator.Refusal()
				if want == "" {
					continue // this combination does not mark the process (the kind variables alone)
				}
				cases++
				if got := opgate.Refusal(); got != want {
					t.Errorf("%s + %s + ACTOR=%q: opgate.Refusal() = %q, operator.Refusal() = %q", a, b, actor, got, want)
					continue
				}
				wantText := fmt.Sprintf("registering a publication from an agent needs an approved send: prepare the send (it is queued for approval, and registers itself once approved), "+
					"or pass --approval <hil id> of a send that was approved and sent. Asked because %s", want)
				if err := registerPublication(t, cfg); err == nil || !strings.Contains(err.Error(), wantText) {
					t.Errorf("%s + %s + ACTOR=%q: register text %v", a, b, actor, err)
				}
			}
		}
	}
	if cases < 20 {
		t.Fatalf("only %d marker combinations were exercised: the matrix is stale", cases)
	}
}

// A registration that fails after the approval was claimed gives the claim back: the same approved send can
// register a valid publication afterwards (the failure is an invalid URL, which the claim does not look at).
func TestPublicationRegisterGivesTheClaimBackWhenTheRegistrationFails(t *testing.T) {
	cfg := sendEnv(t)
	swapRunner(t)
	id := queueViaNodeRun(t, cfg, sendConfig)
	asOperatorProcess(t)
	if _, err := runSendCLI(t, cfg, newHILCmd(cfg), "resolve", id, "--approve"); err != nil {
		t.Fatal(err)
	}
	asOperatorProcess(t)
	opgate.SetForTest(t, cfg.gateRefusal)
	t.Setenv(coderhost.SessionTokenEnv, "session-token")
	register := func(entry string) error {
		cmd := newPublicationCmd(cfg)
		cmd.SetIn(strings.NewReader(entry))
		_, err := runSendCLI(t, cfg, cmd, "register", "--stdin-json", "--approval", id)
		return err
	}
	bad := strings.Replace(pubEntry, `"remote_id":"1"`, `"remote_id":"9","url":"ftp://example.test/x"`, 1)
	if err := register(bad); err == nil || !strings.Contains(err.Error(), "HTTP(S) URLs") {
		t.Fatalf("an invalid publication: %v", err)
	}
	if n := registeredCount(t, cfg); n != 0 {
		t.Fatalf("%d publications after the failed registration", n)
	}
	if err := register(pubEntry); err != nil {
		t.Fatalf("the approval was not given back after the failed registration: %v", err)
	}
	if n := registeredCount(t, cfg); n != 1 {
		t.Fatalf("%d publications, want 1", n)
	}
}
