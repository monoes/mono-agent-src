package browserjev

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

// providerCtx saves provider "local" of a fake kind answering through fb;
// asDefault also makes it the machine default.
func providerCtx(t *testing.T, fb *jevtest.FakeBackend, asDefault bool) context.Context {
	t.Helper()
	db := testdb.Open(t).DB
	t.Cleanup(jevconf.RegisterKind("fake-browserjev", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{
		Name: "local", Kind: "fake-browserjev", BaseURL: "http://localhost", Model: "fake-model",
	}); err != nil {
		t.Fatal(err)
	}
	if asDefault {
		if err := jevconf.SetDefaultProvider(context.Background(), db, "local"); err != nil {
			t.Fatal(err)
		}
	}
	return vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p1")
}

func runWith(ctx context.Context, t *testing.T, cfg map[string]interface{}) map[string]interface{} {
	t.Helper()
	cfg["url"], cfg["goal"] = "https://example.test/", "Search for zurich"
	out, err := testNode(&fakeDriver{}, zurich).Execute(ctx, workflow.NodeInput{}, cfg)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return out[0].Items[0].JSON
}

// A non-TypeSafe default provider drives the run; the schema's api_key
// default and Jev model do not break it.
func TestDefaultProviderDrivesTheRun(t *testing.T) {
	srv := jevtest.NewServer(t, nil)
	fb := jevtest.NewFakeBackend(policyHandler(t, nil))
	ctx := providerCtx(t, fb, true)
	res := runWith(ctx, t, map[string]interface{}{"api_key": "@secret:typesafe", "model": "jev-latest"})
	if res["status"] != "done" || fb.Calls() == 0 || srv.Calls() != 0 {
		t.Fatalf("status %v, fake %d, TypeSafe %d calls", res["status"], fb.Calls(), srv.Calls())
	}
	if m := fb.Requests()[0].Model; m != "fake-model" {
		t.Errorf("model = %q, want the provider's", m)
	}
}

// The node's provider wins over the default, and with nothing configured
// the run is TypeSafe's, as before.
func TestConfigProviderWinsForBrowserJev(t *testing.T) {
	srv := policyServer(t, nil)
	fb := jevtest.NewFakeBackend(policyHandler(t, nil))
	ctx := providerCtx(t, fb, false)
	if res := runWith(ctx, t, map[string]interface{}{"api_key": "k"}); res["status"] != "done" || fb.Calls() != 0 || srv.Calls() == 0 {
		t.Fatalf("default: status %v, fake %d, TypeSafe %d calls", res["status"], fb.Calls(), srv.Calls())
	}
	before := srv.Calls()
	if res := runWith(ctx, t, map[string]interface{}{"api_key": "k", "provider": "local"}); res["status"] != "done" || fb.Calls() == 0 || srv.Calls() != before {
		t.Fatalf("provider local: status %v, fake %d, TypeSafe %d calls", res["status"], fb.Calls(), srv.Calls()-before)
	}
}

// D7: a provider that gives no probability never passes the value gate, so
// the field goes to the text writer instead of a configured value.
func TestNoProbabilityNeverPicksAValue(t *testing.T) {
	fb := jevtest.NewFakeBackend(formHandler(t, func(label string) string { return label }))
	fb.Caps.Probability = jev.ProbNone
	ctx := providerCtx(t, fb, true)
	drv := &formDriver{fields: []formField{{10, "From", "textbox", "text"}}}
	var writes int
	res := runForm(t, ctx, formNode(drv, func(context.Context, map[string]any) (string, error) {
		writes++
		return "typed", nil
	}), "Book a train from Zurich", map[string]interface{}{"From": "Zurich"})
	if res["status"] != "done" || writes != 1 || drv.executed[0] != "f0:typed" {
		t.Fatalf("status=%v writes=%d executed=%v", res["status"], writes, drv.executed)
	}
}
