package orgdecide

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// fakeProviderKind is registered once at a time: tests using it are not parallel.
const fakeProviderKind = "orgdecide-fake"

// saveFakeProvider registers fb as kind fakeProviderKind and saves a
// provider of that kind named name with model model.
func saveFakeProvider(t *testing.T, db *sql.DB, fb jev.Backend, name, model string, in, out float64) {
	t.Helper()
	t.Cleanup(jevconf.RegisterKind(fakeProviderKind, func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	p := jevconf.Provider{Name: name, Kind: fakeProviderKind, BaseURL: "http://localhost", Model: model, USDPerMTokIn: in, USDPerMTokOut: out}
	if err := jevconf.SaveProvider(context.Background(), db, p); err != nil {
		t.Fatal(err)
	}
}

func jevAutonomy(provider string) *Autonomy {
	a := Defaults("p", "growth")
	a.Decider.Kind = orgdesign.DeciderJev
	a.Decider.Threshold = 0.8
	a.Decider.Provider = provider
	return a
}

// A jev decider with its own provider answers from it, labels the resolver
// and rationale "<provider>/<model>", and prices the call at the provider's
// rates; the model decider's model is never sent to the provider.
func TestJevDeciderAnswersFromItsProvider(t *testing.T) {
	db := newDB(t)
	fb := jevtest.NewFakeBackend(jevtest.Fixed(map[string]string{"verdict": "deny"}))
	saveFakeProvider(t, db, fb, "local", "llama3.2:3b", 1, 2)
	s := NewService(db, nil)

	jd, ok := s.NewDecider(jevAutonomy("local")).(*JevDecider)
	if !ok || jd.Client.Provider != "local" || jd.Client.Retries != 1 {
		t.Fatalf("decider = %#v", s.NewDecider(jevAutonomy("local")))
	}
	fallback := &scriptedDecider{}
	jd.Fallback = fallback
	out, err := jd.Decide(context.Background(), gatePrompt("mid"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fallback.prompts) != 0 || out.Verdict.Verdict != "deny" || out.Resolver != "jev:local/llama3.2:3b" {
		t.Fatalf("outcome = %+v", out)
	}
	if !strings.HasPrefix(out.Verdict.Rationale, "jev local/llama3.2:3b: deny p=0.94 (") {
		t.Fatalf("rationale = %q", out.Verdict.Rationale)
	}
	if want := 100*1/1e6 + 1*2/1e6; math.Abs(out.CostUSD-want) > 1e-15 {
		t.Fatalf("cost = %v, want %v", out.CostUSD, want)
	}
	reqs := fb.Requests()
	if len(reqs) != 1 || reqs[0].Model != "llama3.2:3b" {
		t.Fatalf("requests = %+v", reqs)
	}

	// Below the threshold the note carries the same label.
	jd.Threshold = 0.99
	fallback.replies = map[string]string{KindGate: `{"verdict":"approve","rationale":"fine"}`}
	out, err = jd.Decide(context.Background(), gatePrompt("full"))
	if err != nil || !strings.HasPrefix(out.Verdict.Rationale, "jev local/llama3.2:3b: deny p=0.94 below 0.99 (") {
		t.Fatalf("below threshold: %+v %v", out, err)
	}
	// A failing provider too.
	fb.SetErr(errors.New("connection refused"))
	out, err = jd.Decide(context.Background(), gatePrompt("full"))
	if err != nil || !strings.HasPrefix(out.Verdict.Rationale, "jev local/llama3.2:3b failed (") {
		t.Fatalf("failure: %+v %v", out, err)
	}
}

// With no provider on the decider, the profile's decider surface (then the
// machine default) chooses it (plan D9).
func TestJevDeciderFollowsTheDeciderSurface(t *testing.T) {
	db := newDB(t)
	fb := jevtest.NewFakeBackend(nil)
	saveFakeProvider(t, db, fb, "local", "m1", 0, 0)
	if err := jevconf.SetSurfaceProvider(context.Background(), db, "p", jevconf.Decider, "local"); err != nil {
		t.Fatal(err)
	}
	jd, ok := NewService(db, nil).NewDecider(jevAutonomy("")).(*JevDecider)
	if !ok || jd.Client.Provider != "local" || jd.label() != "local/m1" {
		t.Fatalf("decider = %#v", jd)
	}
}

// A provider's own cost figure wins over its rates.
func TestJevDeciderCost(t *testing.T) {
	c := 0.5
	d := &JevDecider{USDPerMTokIn: 1, USDPerMTokOut: 2}
	for _, tc := range []struct {
		name string
		resp jev.Response
		want float64
	}{
		{"typesafe", jev.Response{Provider: jev.ProviderTypeSafe, Usage: jev.Usage{InputTokens: 100, OutputTokens: 5}}, 100 * jevconf.USDPerInputToken},
		{"unnamed is typesafe", jev.Response{Usage: jev.Usage{InputTokens: 100}}, 100 * jevconf.USDPerInputToken},
		{"provider says", jev.Response{Provider: "local", Usage: jev.Usage{InputTokens: 100, CostUSD: &c}}, 0.5},
		{"provider rates", jev.Response{Provider: "local", Usage: jev.Usage{InputTokens: 1e6, OutputTokens: 1e6}}, 3},
	} {
		if got := d.cost(&tc.resp); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("%s: cost = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := (&JevDecider{}).cost(&jev.Response{Provider: "local", Usage: jev.Usage{InputTokens: 100}}); got != 0 {
		t.Errorf("unpriced provider cost = %v", got)
	}
}

// A provider that gives no probability never decides (plan D7): the fallback
// does, and the row keeps no distribution.
func TestJevDeciderProbabilityNoneFallsBack(t *testing.T) {
	db := newDB(t)
	fb := jevtest.NewFakeBackend(jevtest.Fixed(map[string]string{"verdict": "approve"}))
	fb.Caps.Probability = jev.ProbNone
	saveFakeProvider(t, db, fb, "plain", "m", 0, 0)
	jd := NewService(db, nil).NewDecider(jevAutonomy("plain")).(*JevDecider)
	fallback := &scriptedDecider{replies: map[string]string{KindGate: `{"verdict":"deny","rationale":"model"}`}}
	jd.Fallback = fallback
	out, err := jd.Decide(context.Background(), gatePrompt("full"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fallback.prompts) != 1 || out.Verdict.Verdict != "deny" || out.Probabilities != nil || out.Confidence != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if !strings.HasPrefix(out.Verdict.Rationale, "jev plain/m: approve p=0.00 below 0.80") {
		t.Fatalf("rationale = %q", out.Verdict.Rationale)
	}
}

func TestCheckDeciderProvider(t *testing.T) {
	db := newDB(t)
	fb := jevtest.NewFakeBackend(nil)
	saveFakeProvider(t, db, fb, "local", "m", 0, 0)
	noChoice := jevconf.Provider{Name: "nouls", Kind: fakeProviderKind, BaseURL: "http://localhost",
		Caps: jev.Capabilities{Kinds: []string{jev.TypeNoul}}}
	if err := jevconf.SaveProvider(context.Background(), db, noChoice); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "typesafe", "local"} {
		if err := CheckDeciderProvider(db, name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	if err := CheckDeciderProvider(db, "nope"); !errors.Is(err, jevconf.ErrUnknownProvider) {
		t.Errorf("unknown: %v", err)
	}
	if err := CheckDeciderProvider(db, "nouls"); err == nil || !strings.Contains(err.Error(), "choice") {
		t.Errorf("no choice: %v", err)
	}
	if err := CheckDeciderProvider(db, "Bad/Name"); err == nil {
		t.Error("bad name accepted")
	}
	a := jevAutonomy("Bad/Name")
	if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Errorf("Validate: %v", err)
	}
}

// The display copy names a jev decider's provider; reconcile never takes a
// provider from the org file (an agent may edit it, C-54).
func TestDeciderProviderDisplayCopy(t *testing.T) {
	a := jevAutonomy("local")
	if got := DisplayCopy(a, nil).Decider.Provider; got != "local" {
		t.Fatalf("display provider = %q", got)
	}
	a.Decider.Kind = orgdesign.DeciderModel
	if got := DisplayCopy(a, nil).Decider.Provider; got != "" {
		t.Fatalf("model decider displays provider %q", got)
	}

	s := NewStore(newDB(t))
	ctx := context.Background()
	row := jevAutonomy("")
	row.Level = orgdesign.LevelMid
	if err := s.Put(ctx, row, "cli"); err != nil {
		t.Fatal(err)
	}
	doc := &orgdesign.Doc{Name: "growth", Autonomy: &orgdesign.Autonomy{Level: orgdesign.LevelMid,
		Decider: &orgdesign.Decider{Kind: orgdesign.DeciderJev, Provider: "evil"}}}
	if _, err := ReconcileAutonomy(ctx, s, "p", doc); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, "p", "growth")
	if err != nil || got.Decider.Provider != "" || doc.Autonomy.Decider.Provider != "" {
		t.Fatalf("row provider %q, file provider %q, %v", got.Decider.Provider, doc.Autonomy.Decider.Provider, err)
	}
}
