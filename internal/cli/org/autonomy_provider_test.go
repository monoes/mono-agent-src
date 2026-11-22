package org

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/storage"
)

// cliFakeKind is a provider kind registered by one test at a time (not parallel).
const cliFakeKind = "cliorg-fake"

// saveCLIProvider saves provider name of kind cliFakeKind in the fixture's database.
func saveCLIProvider(t *testing.T, f *orgCLIFixture, name string, caps jev.Capabilities) {
	t.Helper()
	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := jevconf.SaveProvider(context.Background(), db.DB,
		jevconf.Provider{Name: name, Kind: cliFakeKind, BaseURL: "http://localhost", Model: "m", Caps: caps}); err != nil {
		t.Fatal(err)
	}
}

// `--decider jev --decider-provider <name>` stores the provider, shows it,
// and needs no TypeSafe key; it is refused for other kinds, for an unknown
// provider and for one that answers no choice questions; "" clears it.
func TestOrgAutonomyDeciderProvider(t *testing.T) {
	f := newOrgCLIFixture(t)
	stubRuntimes(t, nil, nil, nil)
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Cleanup(jevconf.RegisterKind(cliFakeKind, func(jevconf.Provider, string) (jev.Backend, error) {
		return jevtest.NewFakeBackend(nil), nil
	}))
	saveCLIProvider(t, f, "local", jev.Capabilities{})
	saveCLIProvider(t, f, "nouls", jev.Capabilities{Kinds: []string{jev.TypeNoul}})

	set := f.mustRun(t, "autonomy", "set", "growth", "--decider", "jev", "--decider-provider", "local")
	if d := set["decider"].(map[string]interface{}); d["kind"] != "jev" || d["provider"] != "local" {
		t.Fatalf("decider = %v", d)
	}
	if got := f.mustRun(t, "autonomy", "show", "growth")["decider"].(map[string]interface{}); got["provider"] != "local" {
		t.Fatalf("show = %v", got)
	}
	// The row holds it; the org file's decider block keeps monomind's shape.
	if got := f.load(t).Autonomy.Decider.Provider; got != "" {
		t.Fatalf("org file provider = %q", got)
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--decider", "model", "--decider-provider", "local"}, "--decider jev"},
		{[]string{"--decider-provider", "nope"}, "unknown decision provider"},
		{[]string{"--decider-provider", "nouls"}, "choice"},
	} {
		_, err := f.run(t, append([]string{"autonomy", "set", "growth"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: err = %v, want %q", c.args, err, c.want)
		}
	}

	// "" restores the default (here TypeSafe), which then needs its key.
	if _, err := f.run(t, "autonomy", "set", "growth", "--decider-provider", ""); err == nil || !strings.Contains(err.Error(), "TypeSafe API key") {
		t.Fatalf("clear without a key: %v", err)
	}
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	if d := f.mustRun(t, "autonomy", "set", "growth", "--decider-provider", "")["decider"].(map[string]interface{}); d["provider"] != "" {
		t.Fatalf("cleared provider = %v", d["provider"])
	}

	// A model decider shows no provider.
	f.mustRun(t, "autonomy", "set", "growth", "--decider", "model")
	if got := f.mustRun(t, "autonomy", "show", "growth")["decider"].(map[string]interface{}); got["provider"] != "" {
		t.Fatalf("model decider provider = %v", got["provider"])
	}
}

// The decision row's jev summary: old TypeSafe rows unchanged, provider rows
// name the provider, whatever ":", "/", "." and "-" the model name holds.
func TestJevViewProvider(t *testing.T) {
	probs := map[string]float64{"approve": 0.62, "deny": 0.38}
	for _, c := range []struct {
		name     string
		d        orgdecide.Decision
		provider string
		decided  bool
		th       float64
	}{
		{"typesafe decided", orgdecide.Decision{Resolver: "jev:jev-1.13", Rationale: "jev jev-1.13: approve p=0.94 (approve 0.94, deny 0.06)", Probabilities: probs}, "", true, -1},
		{"typesafe below", orgdecide.Decision{Resolver: "model:claude-fable-5-1", Rationale: "jev jev-1.13: approve p=0.62 below 0.80 (approve 0.62, deny 0.38); weekend", Probabilities: probs}, "", false, 0.8},
		{"provider decided", orgdecide.Decision{Resolver: "jev:local/llama3.2:3b", Rationale: "jev local/llama3.2:3b: approve p=0.94 (…)", Probabilities: probs}, "local", true, -1},
		{"provider below", orgdecide.Decision{Resolver: "model:claude-fable-5-1", Rationale: "jev local/llama3.2:3b: approve p=0.62 below 0.75 (…); no", Probabilities: probs}, "local", false, 0.75},
		{"slashed model decided", orgdecide.Decision{Resolver: "jev:openrouter/openai/gpt-4.1-mini", Rationale: "jev openrouter/openai/gpt-4.1-mini: approve p=0.91 (…)", Probabilities: probs}, "openrouter", true, -1},
		{"slashed model below", orgdecide.Decision{Resolver: "model:m", Rationale: "jev openrouter/openai/gpt-4.1-mini: deny p=0.51 below 0.90 (…); x", Probabilities: probs}, "openrouter", false, 0.9},
		{"provider failed", orgdecide.Decision{Resolver: "model:m", Rationale: "jev my-box-2/qwen2.5-7b failed (connection refused); ok", Probabilities: probs}, "my-box-2", false, -1},
	} {
		v := jevView(c.d)
		if v == nil {
			t.Errorf("%s: no view", c.name)
			continue
		}
		if v.Provider != c.provider || v.Decided != c.decided || v.Verdict != "approve" || v.P != 0.62 {
			t.Errorf("%s: view = %+v", c.name, v)
		}
		if (c.th < 0) != (v.Threshold == nil) || (v.Threshold != nil && *v.Threshold != c.th) {
			t.Errorf("%s: threshold = %v, want %v", c.name, v.Threshold, c.th)
		}
		raw, _ := json.Marshal(v)
		if c.provider == "" && strings.Contains(string(raw), "provider") {
			t.Errorf("%s: TypeSafe row carries a provider: %s", c.name, raw)
		}
	}
	if v := jevView(orgdecide.Decision{Resolver: "model:m", Rationale: "jev x/y: approve"}); v != nil {
		t.Errorf("row without jev probabilities got a view: %+v", v)
	}
}
