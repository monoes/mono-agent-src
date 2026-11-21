package choose

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// fakeProvider saves provider "local" of a fake kind answering through fb.
func fakeProvider(t *testing.T, fb *jevtest.FakeBackend) context.Context {
	t.Helper()
	db := testdb.Open(t).DB
	t.Cleanup(jevconf.RegisterKind("fake-choose", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{
		Name: "local", Kind: "fake-choose", BaseURL: "http://localhost", Model: "fake-model",
	}); err != nil {
		t.Fatal(err)
	}
	return vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
}

// With a non-TypeSafe default provider the node answers from it, and the
// schema's api_key default (@secret:typesafe) and Jev model do not break it.
func TestDefaultProviderAnswers(t *testing.T) {
	srv := jevtest.NewServer(t, nil)
	fb := jevtest.NewFakeBackend(routeByText)
	ctx := fakeProvider(t, fb)
	if err := jevconf.SetDefaultProvider(context.Background(), vault.DBFromContext(ctx), "local"); err != nil {
		t.Fatal(err)
	}
	cfg := baseConfig()
	cfg["api_key"], cfg["model"] = "@secret:typesafe", "jev-latest"
	out, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("a tech crash")}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := handles(out)["support"]
	if len(got) != 1 {
		t.Fatalf("routing = %v", handles(out))
	}
	res := got[0].JSON["choice"].(map[string]any)
	if res["provider"] != "local" || res["model"] != "fake-model" || res["probability_kind"] != "logprob" {
		t.Errorf("result = %v", res)
	}
	if fb.Calls() != 1 || srv.Calls() != 0 {
		t.Errorf("fake %d, TypeSafe %d calls", fb.Calls(), srv.Calls())
	}
}

// A provider named in the node's config wins over the default (TypeSafe).
func TestConfigProviderWins(t *testing.T) {
	srv := jevtest.NewServer(t, nil)
	fb := jevtest.NewFakeBackend(routeByText)
	ctx := fakeProvider(t, fb)
	cfg := baseConfig()
	cfg["provider"] = "local"
	if _, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("about billing")}, cfg); err != nil {
		t.Fatal(err)
	}
	if fb.Calls() != 1 || srv.Calls() != 0 {
		t.Errorf("fake %d, TypeSafe %d calls", fb.Calls(), srv.Calls())
	}
	// No provider anywhere: TypeSafe, exactly as before, labelled as such.
	delete(cfg, "provider")
	out, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("about billing")}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	res := handles(out)["billing"][0].JSON["choice"].(map[string]any)
	if srv.Calls() != 1 || res["provider"] != "typesafe" || res["probability_kind"] != "native" {
		t.Errorf("TypeSafe calls %d, result %v", srv.Calls(), res)
	}
}

// D7: a provider that gives no probability never passes min_confidence.
func TestNoProbabilityGoesToLowConfidence(t *testing.T) {
	fb := jevtest.NewFakeBackend(routeByText)
	fb.Caps.Probability = jev.ProbNone
	ctx := fakeProvider(t, fb)
	cfg := baseConfig()
	cfg["provider"] = "local"
	out, err := (&Node{}).Execute(ctx, workflow.NodeInput{Items: items("about billing")}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := handles(out)
	if len(h[LowConfidenceHandle]) != 1 || len(h["billing"]) != 0 {
		t.Fatalf("routing = %v", h)
	}
	res := h[LowConfidenceHandle][0].JSON["choice"].(map[string]any)
	if res["choice"] != "billing" || res["probability"] != 0.0 || res["probability_kind"] != "none" {
		t.Errorf("result = %v", res)
	}
}
