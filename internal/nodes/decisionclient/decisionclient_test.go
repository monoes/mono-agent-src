package decisionclient

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/jev/jevtest/callers"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

var surface = jevconf.NodeSurface("ai.choose")

// secretsAdd stores a provider key in the machine vault.
func secretsAdd(db *sql.DB, name string) (string, error) {
	return secrets.Add(context.Background(), db, jevconf.VaultScope, "secret", name, map[string]string{"secret": "k-0123456789"}, "", "", "")
}

// withFake registers a fake provider "local" (model "fake-model"); it does
// not make it the default.
func withFake(t *testing.T, db *sql.DB) *jevtest.FakeBackend {
	t.Helper()
	fb := jevtest.NewFakeBackend(nil)
	t.Cleanup(jevconf.RegisterKind("fake-decisionclient", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{
		Name: "local", Kind: "fake-decisionclient", BaseURL: "http://localhost", Model: "fake-model",
	}); err != nil {
		t.Fatal(err)
	}
	return fb
}

// D4: nothing configured is today's TypeSafe client, with the node's key and model.
func TestNothingConfiguredIsTodaysTypeSafeClient(t *testing.T) {
	db := testdb.Open(t).DB
	c, provider, err := New(context.Background(), db, "p1", Config{APIKey: "node-key", Model: "jev-2"}, surface)
	if err != nil {
		t.Fatal(err)
	}
	if provider != jev.ProviderTypeSafe || c.Provider != jev.ProviderTypeSafe || c.Backend != nil {
		t.Errorf("provider = %q / %q, backend %v", provider, c.Provider, c.Backend)
	}
	if c.APIKey != "node-key" || c.Model != "jev-2" {
		t.Errorf("key/model = %q/%q, want the node's", c.APIKey, c.Model)
	}
}

// A default provider other than TypeSafe drops the node's api_key (the
// schema default @secret:typesafe must not break the node) and its Jev
// default model, so the provider's own model applies.
func TestDefaultProviderIgnoresTheTypeSafeKeyAndDefaultModel(t *testing.T) {
	db := testdb.Open(t).DB
	fb := withFake(t, db)
	if err := jevconf.SetDefaultProvider(context.Background(), db, "local"); err != nil {
		t.Fatal(err)
	}
	c, provider, err := New(context.Background(), db, "p1", Config{APIKey: "@secret:typesafe", Model: jev.DefaultModel}, surface)
	if err != nil {
		t.Fatal(err)
	}
	if provider != "local" || c.Provider != "local" || c.Model != "fake-model" {
		t.Errorf("provider %q/%q model %q", provider, c.Provider, c.Model)
	}
	if _, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": {Type: jev.TypeNoul}}); err != nil {
		t.Fatal(err)
	}
	if fb.Calls() != 1 {
		t.Errorf("fake calls = %d", fb.Calls())
	}
	// S9: a provider the node inherited answers with its record's model,
	// whatever the node's model says.
	c, _, err = New(context.Background(), db, "p1", Config{Model: "llama3"}, surface)
	if err != nil || c.Model != "fake-model" {
		t.Errorf("inherited provider, node model: %v, %v", c, err)
	}
	// Naming the provider too makes the node's model apply; a Jev model name never does.
	if c, _, err = New(context.Background(), db, "p1", Config{Provider: "local", Model: "llama3"}, surface); err != nil || c.Model != "llama3" {
		t.Errorf("named provider and model: %v, %v", c, err)
	}
	if c, _, err = New(context.Background(), db, "p1", Config{Provider: "local", Model: "jev-2"}, surface); err != nil || c.Model != "fake-model" {
		t.Errorf("a Jev model name went to another provider: %v, %v", c, err)
	}
}

// S8: ai.choose, ai.decide and HIL auto_decide count as read-only for an
// org's grants, so in a run an AI started or wrote they may not send data to
// a provider on another host (TypeSafe and this machine's stay allowed).
// Other decision nodes (browser.jev, applications.evaluate) are outbound
// for grants already and are not refused here.
func TestARemoteProviderIsRefusedToReadOnlyNodesInAIRuns(t *testing.T) {
	db := testdb.Open(t).DB
	withFake(t, db)
	if _, err := secretsAdd(db, "decision-remote"); err != nil {
		t.Fatal(err)
	}
	if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{
		Name: "remote", Kind: "fake-decisionclient", BaseURL: "https://decide.example", SecretName: "decision-remote",
	}); err != nil {
		t.Fatal(err)
	}
	base := context.Background()
	runs := map[string]context.Context{
		"granted org call": workflow.WithTrigger(base, workflow.TriggerTypeOrgTool, nil),
		"org message":      workflow.WithTrigger(base, workflow.TriggerTypeOrgMessage, nil),
		"trigger.org":      workflow.WithTrigger(base, workflow.TriggerNodeTypeOrg, nil),
		"AI-authored":      workflow.WithAIAuthored(base),
	}
	for name, ctx := range runs {
		for _, s := range []jevconf.Surface{jevconf.NodeSurface("ai.choose"), jevconf.NodeSurface("ai.decide"), jevconf.HIL} {
			if _, _, err := New(ctx, db, "p1", Config{Provider: "remote"}, s); !errors.Is(err, ErrRemoteFromAIRun) {
				t.Errorf("%s, %s: %v", name, s, err)
			}
			if _, _, err := New(ctx, db, "p1", Config{Provider: "local"}, s); err != nil {
				t.Errorf("%s, %s, loopback provider: %v", name, s, err)
			}
		}
		if _, _, err := New(ctx, db, "p1", Config{Provider: "remote"}, jevconf.NodeSurface("browser.jev")); errors.Is(err, ErrRemoteFromAIRun) {
			t.Errorf("%s: browser.jev was refused: %v", name, err)
		}
	}
	// Inherited from the default, the same.
	if err := jevconf.SetDefaultProvider(base, db, "remote"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(runs["granted org call"], db, "p1", Config{}, surface); !errors.Is(err, ErrRemoteFromAIRun) {
		t.Errorf("inherited remote provider: %v", err)
	}
	// The user's own run uses it.
	if _, _, err := New(base, db, "p1", Config{}, surface); err != nil {
		t.Errorf("the user's run: %v", err)
	}
}

// The node's own provider wins over the default, both ways.
func TestExplicitProviderWins(t *testing.T) {
	db := testdb.Open(t).DB
	withFake(t, db)
	c, provider, err := New(context.Background(), db, "p1", Config{Provider: "local", APIKey: "node-key"}, surface)
	if err != nil || provider != "local" || c.Provider != "local" {
		t.Fatalf("explicit local: %v %q %v", c, provider, err)
	}
	if err := jevconf.SetDefaultProvider(context.Background(), db, "local"); err != nil {
		t.Fatal(err)
	}
	c, provider, err = New(context.Background(), db, "p1", Config{Provider: "typesafe", APIKey: "node-key"}, surface)
	if err != nil || provider != jev.ProviderTypeSafe || c.Provider != jev.ProviderTypeSafe || c.APIKey != "node-key" {
		t.Fatalf("explicit typesafe: %v %q %v", c, provider, err)
	}
	if _, _, err := New(context.Background(), db, "p1", Config{Provider: "nope"}, surface); err == nil {
		t.Error("an unknown provider must fail")
	}
}

// A surface's own provider choice applies when the node names none.
func TestSurfaceProviderApplies(t *testing.T) {
	db := testdb.Open(t).DB
	withFake(t, db)
	if err := jevconf.SetSurfaceProvider(context.Background(), db, "p1", surface, "local"); err != nil {
		t.Fatal(err)
	}
	if _, provider, err := New(context.Background(), db, "p1", Config{APIKey: "@secret:typesafe"}, surface); err != nil || provider != "local" {
		t.Fatalf("provider %q, %v", provider, err)
	}
	if _, provider, _ := New(context.Background(), db, "p2", Config{APIKey: "k"}, surface); provider != jev.ProviderTypeSafe {
		t.Errorf("another profile got %q", provider)
	}
}

func TestGate(t *testing.T) {
	for _, c := range []struct {
		resp *jev.Response
		want float64
	}{
		{nil, 0.8},
		{&jev.Response{}, 0.8},
		{&jev.Response{Probability: jev.ProbNative}, 0.8},
		{&jev.Response{Probability: jev.ProbLogprob}, 0.8},
		{&jev.Response{Probability: jev.ProbNone}, 0},
	} {
		if got := Gate(c.resp, 0.8); got != c.want {
			t.Errorf("Gate(%+v) = %v, want %v", c.resp, got, c.want)
		}
	}
}

// A node's explicit provider from an unapproved step: a keyed provider's key
// is refused, a remote one is refused before that, and nothing is asked.
func TestAnExplicitProviderFromARefusedCaller(t *testing.T) {
	for _, c := range callers.All() {
		if !c.Refused {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			db := testdb.Open(t).DB
			fb := withFake(t, db)
			if _, err := secretsAdd(db, "decision-keyed"); err != nil {
				t.Fatal(err)
			}
			for _, p := range []jevconf.Provider{
				{Name: "keyed", Kind: "fake-decisionclient", BaseURL: "http://127.0.0.1:9", SecretName: "decision-keyed"},
				{Name: "remote", Kind: "fake-decisionclient", BaseURL: "https://decide.example", SecretName: "decision-keyed2"},
			} {
				if err := jevconf.SaveProvider(context.Background(), db, p); err != nil {
					t.Fatal(err)
				}
			}
			c.Prepare(t)
			ctx := c.Ctx(t, db, context.Background(), "p1", "ai.decide")
			s := jevconf.NodeSurface("ai.decide")
			if _, _, err := New(ctx, db, "p1", Config{Provider: "keyed"}, s); !errors.Is(err, secrets.ErrResolveRefused) {
				t.Errorf("keyed: %v", err)
			}
			if _, _, err := New(ctx, db, "p1", Config{Provider: "remote"}, s); !errors.Is(err, ErrRemoteFromAIRun) {
				t.Errorf("remote: %v", err)
			}
			if fb.Calls() != 0 {
				t.Errorf("%d request(s) sent", fb.Calls())
			}
		})
	}
}
