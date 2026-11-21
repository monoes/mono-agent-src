package decisionclient

import (
	"context"
	"database/sql"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
)

func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

var surface = jevconf.NodeSurface("ai.choose")

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
	// A model the user set is passed on.
	c, _, err = New(context.Background(), db, "p1", Config{Model: "llama3"}, surface)
	if err != nil || c.Model != "llama3" {
		t.Errorf("model = %v, %v", c, err)
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
