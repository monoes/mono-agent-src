package jevconf

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/secrets"
)

var (
	kindSeq   atomic.Int64
	testEpoch = time.Unix(0, 0)
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// fakeKind registers a kind unique to the test whose backend is fb, and
// reports the key each build was given.
func fakeKind(t *testing.T, fb jev.Backend, keys *[]string) string {
	t.Helper()
	k := fmt.Sprintf("fake-%d", kindSeq.Add(1))
	t.Cleanup(RegisterKind(k, func(p Provider, key string) (jev.Backend, error) {
		if keys != nil {
			*keys = append(*keys, key)
		}
		return fb, nil
	}))
	return k
}

func saveLocal(t *testing.T, db *sql.DB, name, kind, secret string) Provider {
	t.Helper()
	p := Provider{Name: name, Kind: kind, BaseURL: "http://localhost:11434/v1/", Model: "m-" + name, SecretName: secret,
		USDPerMTokIn: 2, USDPerMTokOut: 10}
	if err := SaveProvider(context.Background(), db, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// D4: with no provider configured every surface's provider is TypeSafe, and
// the client is today's: no backend, the TypeSafe host.
func TestWithNoProviderConfiguredEverythingIsTypeSafe(t *testing.T) {
	db := newDB(t)
	ts := jevtest.TypeSafeHost(t)
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	for _, s := range append(Surfaces, Decider, DynamicOrg, CLI, NodeSurface("ai.choose")) {
		if p := ActiveProvider(db, "default", s); p != jev.ProviderTypeSafe {
			t.Errorf("%s: active provider %q", s, p)
		}
	}
	if p := ActiveProvider(nil, "", HIL); p != jev.ProviderTypeSafe {
		t.Errorf("no db: %q", p)
	}
	ps, err := Providers(db)
	if err != nil || len(ps) != 1 || !ps[0].Builtin() || ps[0].BaseURL != ts.URL {
		t.Fatalf("providers = %+v %v", ps, err)
	}
	c, src, err := ResolveClient(context.Background(), db, "default", "", "")
	if err != nil || c.Backend != nil || c.BaseURL != ts.URL || c.Provider != jev.ProviderTypeSafe || src != SourceEnv {
		t.Fatalf("ResolveClient = %+v %q %v", c, src, err)
	}
	c, err = NewClient(context.Background(), db, "default", "cfg-key", "", HIL)
	if err != nil || c.Backend != nil || c.BaseURL != ts.URL || c.APIKey != "cfg-key" {
		t.Fatalf("NewClient = %+v %v", c, err)
	}
	askOnce(context.Background(), c)
	// Today's row: the new columns stay NULL (NULL provider is TypeSafe), so
	// a table without them still records.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM jev_usage WHERE surface = 'hil' AND provider IS NULL AND output_tokens IS NULL
		AND probability_kind IS NULL AND cost_usd IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("usage rows of today's shape: %d %v", n, err)
	}
	if _, err := db.Exec(`ALTER TABLE jev_usage DROP COLUMN cost_usd`); err != nil {
		t.Fatal(err)
	}
	askOnce(context.Background(), c)
	if err := db.QueryRow(`SELECT COUNT(*) FROM jev_usage`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("a table without the new columns: %d rows %v", n, err)
	}
}

func TestSaveProviderChecksTheRecord(t *testing.T) {
	db := newDB(t)
	kind := fakeKind(t, jevtest.NewFakeBackend(nil), nil)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		p    Provider
		want string
	}{
		{"reserved name", Provider{Name: "typesafe", Kind: kind, BaseURL: "https://x.example"}, "built-in"},
		{"reserved kind", Provider{Name: "ts2", Kind: "typesafe", BaseURL: "https://x.example"}, "built-in"},
		{"bad name", Provider{Name: "My Box", Kind: kind, BaseURL: "https://x.example"}, "provider name"},
		{"long name", Provider{Name: strings.Repeat("a", 33), Kind: kind, BaseURL: "https://x.example"}, "provider name"},
		{"unknown kind", Provider{Name: "x", Kind: "nope", BaseURL: "https://x.example", SecretName: "k"}, "not in this build"},
		{"http to the LAN", Provider{Name: "x", Kind: kind, BaseURL: "http://192.168.1.5:11434/v1"}, "base_url refused"},
		{"credentials in the URL", Provider{Name: "x", Kind: kind, BaseURL: "https://u:pw@x.example"}, "base_url refused"},
		{"no URL", Provider{Name: "x", Kind: kind}, "base_url refused"},
		{"the TypeSafe key", Provider{Name: "x", Kind: kind, BaseURL: "https://x.example", SecretName: "typesafe"}, "TypeSafe key"},
		{"a TypeSafe key alias", Provider{Name: "x", Kind: kind, BaseURL: "https://x.example", SecretName: "Jev API key"}, "TypeSafe key"},
		{"a reference", Provider{Name: "x", Kind: kind, BaseURL: "https://x.example", SecretName: "@secret:openai"}, "TypeSafe key"},
		{"a bad probability kind", Provider{Name: "x", Kind: kind, BaseURL: "https://x.example", SecretName: "k", Caps: jev.Capabilities{Probability: "vibes"}}, "probability"},
		{"keyless on another host", Provider{Name: "x", Kind: kind, BaseURL: "https://x.example"}, "another host needs a key"},
	} {
		err := SaveProvider(ctx, db, c.p)
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "pw@") {
			t.Errorf("%s: %v, want an error saying %q", c.name, err, c.want)
		}
	}
	if ps, _ := Providers(db); len(ps) != 1 {
		t.Fatalf("a refused provider was stored: %+v", ps)
	}

	saveLocal(t, db, "local", kind, "openai")
	// Saving the same name replaces it; the base URL is stored without its trailing slash.
	if err := SaveProvider(ctx, db, Provider{Name: "local", Kind: kind, BaseURL: "https://api.example/v1/", SecretName: "openai"}); err != nil {
		t.Fatal(err)
	}
	p, ok, err := FindProvider(db, "local")
	if err != nil || !ok || p.BaseURL != "https://api.example/v1" {
		t.Fatalf("FindProvider = %+v %v %v", p, ok, err)
	}
	// Another provider at the same host may share the key; one at another host may not.
	if err := SaveProvider(ctx, db, Provider{Name: "local-big", Kind: kind, BaseURL: "https://api.example/v2", SecretName: "openai"}); err != nil {
		t.Fatalf("same host: %v", err)
	}
	if err := SaveProvider(ctx, db, Provider{Name: "evil", Kind: kind, BaseURL: "https://evil.example", SecretName: "openai"}); err == nil ||
		!strings.Contains(err.Error(), "its own host") {
		t.Fatalf("another host took a configured key: %v", err)
	}
	ps, _ := Providers(db)
	if len(ps) != 3 || ps[0].Name != "typesafe" || ps[1].Name != "local" || ps[2].Name != "local-big" {
		t.Fatalf("providers = %+v", ps)
	}
}

// Adding, removing and choosing a provider decides where data and keys go:
// only the operator's own terminal may.
func TestOnlyTheOperatorConfiguresProviders(t *testing.T) {
	db := newDB(t)
	kind := fakeKind(t, jevtest.NewFakeBackend(nil), nil)
	saveLocal(t, db, "local", kind, "")
	p := Provider{Name: "other", Kind: kind, BaseURL: "https://x.example"}
	check := func(name string, ctx context.Context) {
		t.Helper()
		for what, err := range map[string]error{
			"save":     SaveProvider(ctx, db, p),
			"remove":   RemoveProvider(ctx, db, "local"),
			"default":  SetDefaultProvider(ctx, db, "local"),
			"surface":  SetSurfaceProvider(ctx, db, "default", HIL, "local"),
			"clearing": SetSurfaceProvider(ctx, db, "default", HIL, ""),
		} {
			if !errors.Is(err, ErrOperatorOnly) {
				t.Errorf("%s: %s: %v", name, what, err)
			}
		}
	}
	check("an unapproved step", secrets.RefuseResolving(context.Background(), "an AI wrote this step"))
	t.Run("a marked agent process", func(t *testing.T) {
		t.Setenv("CLAUDECODE", "1")
		check("a marked agent process", context.Background())
	})
	if ps, _ := Providers(db); len(ps) != 2 || DefaultProvider(db) != jev.ProviderTypeSafe {
		t.Fatalf("a refused change landed: %+v", ps)
	}
}

// D9: the call's provider, else the profile's for the surface, else the
// machine default, else TypeSafe.
func TestProviderChoiceIsLayered(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	kind := fakeKind(t, jevtest.NewFakeBackend(nil), nil)
	saveLocal(t, db, "a", kind, "")
	saveLocal(t, db, "b", kind, "")
	if err := SetDefaultProvider(ctx, db, "nope"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("an unknown default: %v", err)
	}
	if err := SetSurfaceProvider(ctx, db, "default", Surface("bogus"), "a"); err == nil {
		t.Fatal("an unknown surface was accepted")
	}
	if err := SetSurfaceProvider(ctx, db, "", HIL, "a"); err == nil {
		t.Fatal("a surface choice without a profile was accepted")
	}
	if err := SetDefaultProvider(ctx, db, "a"); err != nil {
		t.Fatal(err)
	}
	if err := SetSurfaceProvider(ctx, db, "default", HIL, "b"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		profile string
		s       Surface
		want    string
	}{{"default", HIL, "b"}, {"default", Inbox, "a"}, {"other", HIL, "a"}, {"", HIL, "a"}} {
		if got := ActiveProvider(db, c.profile, c.s); got != c.want {
			t.Errorf("%q/%s: %q, want %q", c.profile, c.s, got, c.want)
		}
	}
	for _, c := range []struct {
		call, want string
		s          Surface
	}{{"", "b", HIL}, {"", "a", Inbox}, {"a", "a", HIL}, {"typesafe", "typesafe", HIL}} {
		t.Setenv("TYPESAFE_API_KEY", "env-key")
		cl, err := NewClientFor(ctx, db, "default", c.call, "", "", c.s)
		if err != nil || cl.Provider != c.want {
			t.Errorf("call %q on %s: %+v %v, want %q", c.call, c.s, cl, err, c.want)
		}
	}
	if c, _, err := ResolveClient(ctx, db, "default", "", ""); err != nil || c.Provider != "a" || c.Model != "m-a" {
		t.Fatalf("ResolveClient follows the machine default: %+v %v", c, err)
	}
	// A surface set to typesafe overrides a non-TypeSafe default; clearing it restores the default.
	if err := SetSurfaceProvider(ctx, db, "default", HIL, "typesafe"); err != nil || ActiveProvider(db, "default", HIL) != "typesafe" {
		t.Fatalf("typesafe on a surface: %v", err)
	}
	if err := SetSurfaceProvider(ctx, db, "default", HIL, ""); err != nil || ActiveProvider(db, "default", HIL) != "a" {
		t.Fatalf("cleared: %v", err)
	}
	if err := SetDefaultProvider(ctx, db, ""); err != nil || DefaultProvider(db) != "typesafe" {
		t.Fatalf("default restored: %v", err)
	}
}

// A provider choice that cannot be read is an error, never TypeSafe by
// default: the surface may have been moved off TypeSafe.
func TestAnUnreadableProviderChoiceFailsClosed(t *testing.T) {
	db := newDB(t)
	ts := jevtest.TypeSafeHost(t)
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	if _, err := db.Exec(`DROP TABLE settings`); err != nil {
		t.Fatal(err)
	}
	if c, err := NewClient(context.Background(), db, "default", "", "", HIL); err == nil || c != nil {
		t.Fatalf("NewClient = %+v %v", c, err)
	}
	if c, _, err := ResolveClient(context.Background(), db, "default", "", ""); err == nil || c != nil {
		t.Fatalf("ResolveClient = %+v %v", c, err)
	}
	if ActiveProvider(db, "default", HIL) != jev.ProviderTypeSafe || ts.Calls() != 0 {
		t.Fatal("display or a send went wrong")
	}
}

func TestRemoveProviderRefusesAProviderInUse(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	kind := fakeKind(t, jevtest.NewFakeBackend(nil), nil)
	saveLocal(t, db, "a", kind, "")
	if err := RemoveProvider(ctx, db, "typesafe"); err == nil {
		t.Fatal("typesafe was removed")
	}
	if err := RemoveProvider(ctx, db, "nope"); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("unknown: %v", err)
	}
	_ = SetDefaultProvider(ctx, db, "a")
	_ = SetSurfaceProvider(ctx, db, "default", Capture, "a")
	err := RemoveProvider(ctx, db, "a")
	if err == nil || !strings.Contains(err.Error(), "jev.provider") || !strings.Contains(err.Error(), "jev.default.capture.provider") {
		t.Fatalf("removing a provider in use: %v", err)
	}
	_ = SetDefaultProvider(ctx, db, "")
	_ = SetSurfaceProvider(ctx, db, "default", Capture, "")
	if err := RemoveProvider(ctx, db, "a"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := FindProvider(db, "a"); ok {
		t.Fatal("still there")
	}
}

// An org's jev decider that names the provider keeps it, by org; a decider
// naming another provider, or unparsable JSON, does not; a database without
// org_autonomy has no org using it.
func TestRemoveProviderRefusesAProviderAnOrgDeciderNames(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	kind := fakeKind(t, jevtest.NewFakeBackend(nil), nil)
	saveLocal(t, db, "a", kind, "")
	saveLocal(t, db, "b", kind, "")
	if _, err := db.Exec(`INSERT INTO org_autonomy (profile_id, org_name, level, decider_json, updated_at, updated_by) VALUES
		('default', 'acme', 'full', '{"kind":"jev","provider":"a"}', 'x', 'cli'),
		('default', 'beta', 'full', '{"kind":"jev","provider":"b"}', 'x', 'cli'),
		('p2', 'gamma', 'full', '{"kind":"jev","provider" "a"', 'x', 'cli'),
		('p2', 'delta', 'mid', '{"kind":"model","model":"a"}', 'x', 'cli'),
		('p2', 'eps', 'mid', '{"kind":"boss","provider":"a"}', 'x', 'cli')`); err != nil {
		t.Fatal(err)
	}
	err := RemoveProvider(ctx, db, "a")
	if err == nil || !strings.Contains(err.Error(), "org decider of default/acme") || strings.Contains(err.Error(), "beta") ||
		strings.Contains(err.Error(), "gamma") || strings.Contains(err.Error(), "delta") || strings.Contains(err.Error(), "eps") {
		t.Fatalf("removing a provider an org decider names: %v", err)
	}
	if _, ok, _ := FindProvider(db, "a"); !ok {
		t.Fatal("a refused removal removed it")
	}
	if _, err := db.Exec(`UPDATE org_autonomy SET decider_json = '{"kind":"jev"}' WHERE org_name = 'acme'`); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProvider(ctx, db, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE org_autonomy`); err != nil {
		t.Fatal(err)
	}
	if err := RemoveProvider(ctx, db, "b"); err != nil {
		t.Fatalf("no org_autonomy table: %v", err)
	}
}

func TestAProvidersClientIsBuiltFromItsRecordAndKey(t *testing.T) {
	keyring.MockInit()
	db := newDB(t)
	ctx := context.Background()
	fb := jevtest.NewFakeBackend(nil)
	var keys []string
	kind := fakeKind(t, fb, &keys)
	saveLocal(t, db, "keyed", kind, "openai")
	saveLocal(t, db, "keyless", kind, "")
	t.Setenv("TYPESAFE_API_KEY", "env-key")

	// The key is only the machine vault entry: not a profile's, not the environment.
	seedKey(t, db, "default", "openai", "profile-openai", "2026-01-01T00:00:00Z")
	_, _, err := ResolveProviderClient(ctx, db, "default", "keyed", "", "")
	if !errors.Is(err, jev.ErrNoAPIKey) || !strings.Contains(err.Error(), `"openai"`) || strings.Contains(err.Error(), "TYPESAFE") {
		t.Fatalf("no machine key: %v", err)
	}
	seedKey(t, db, VaultScope, "openai", "machine-openai", "2026-01-01T00:00:00Z")
	c, src, err := ResolveProviderClient(ctx, db, "default", "keyed", "", "")
	if err != nil || src != SourceVault || c.Backend != fb || c.Model != "m-keyed" || keys[len(keys)-1] != "machine-openai" {
		t.Fatalf("keyed = %+v %q %v %v", c, src, err, keys)
	}
	if c, src, err := ResolveProviderClient(ctx, db, "default", "keyless", "", "chosen"); err != nil || src != SourceKeyless ||
		c.Model != "chosen" || keys[len(keys)-1] != "" {
		t.Fatalf("keyless = %+v %q %v", c, src, err)
	}
	// A step's own key never goes to another provider (D5), however it is written.
	for _, explicit := range []string{"sk-live", "@secret:openai", "@secret:typesafe"} {
		if _, _, err := ResolveProviderClient(ctx, db, "default", "keyed", explicit, ""); !errors.Is(err, ErrExplicitKeyWithProvider) {
			t.Errorf("explicit %q: %v", explicit, err)
		}
	}
	if _, err := NewClientFor(ctx, db, "default", "keyless", "cfg", "", HIL); !errors.Is(err, ErrExplicitKeyWithProvider) {
		t.Errorf("NewClientFor with a step key: %v", err)
	}
	if _, _, err := ResolveProviderClient(ctx, db, "default", "gone", "", ""); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("unknown: %v", err)
	}
	// The stored record is checked again when used: the settings table is plaintext.
	if _, err := db.Exec(`UPDATE settings SET value = replace(value, 'http://localhost:11434', 'http://10.0.0.9') WHERE key = 'jev.providers'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveProviderClient(ctx, db, "default", "keyless", "", ""); !errors.Is(err, ErrProviderURL) {
		t.Fatalf("a tampered base_url: %v", err)
	}
}

func TestAProviderWhoseKindIsMissingFailsByName(t *testing.T) {
	db := newDB(t)
	unregister := RegisterKind("gone-kind", func(Provider, string) (jev.Backend, error) { return jevtest.NewFakeBackend(nil), nil })
	saveLocal(t, db, "x", "gone-kind", "")
	unregister()
	_, _, err := ResolveProviderClient(context.Background(), db, "default", "x", "", "")
	if err == nil || !strings.Contains(err.Error(), `"gone-kind"`) {
		t.Fatalf("err = %v", err)
	}
	for _, bad := range []string{"typesafe", "Bad", ""} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("RegisterKind(%q) did not panic", bad)
				}
			}()
			RegisterKind(bad, func(Provider, string) (jev.Backend, error) { return nil, nil })
		}()
	}
}

// Every call of a provider's client is recorded with its provider, output
// tokens, probability kind and cost (the provider's own, else its rates).
func TestAProvidersUsageIsRecordedAndPriced(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	fb := jevtest.NewFakeBackend(nil)
	kind := fakeKind(t, fb, nil)
	saveLocal(t, db, "local", kind, "")
	if err := SetDefaultProvider(ctx, db, "local"); err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(ctx, db, "default", "", "", HIL)
	if err != nil {
		t.Fatal(err)
	}
	askOnce(ctx, c)
	reported := 0.5
	fb.SetRespond(func(req *jev.Request) (*jev.Response, error) {
		a, _ := jevtest.Answer(req.Questions["q"], "a")
		return &jev.Response{Answers: map[string]jev.Answer{"q": a}, Usage: jev.Usage{InputTokens: 1, CostUSD: &reported}}, nil
	})
	askOnce(ctx, c)
	fb.SetErr(errors.New("down"))
	askOnce(ctx, c)

	rows, err := db.Query(`SELECT provider, output_tokens, probability_kind, cost_usd, ok FROM jev_usage ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var p string
		var out sql.NullInt64
		var kind sql.NullString
		var cost sql.NullFloat64
		var ok int
		if err := rows.Scan(&p, &out, &kind, &cost, &ok); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %v %v %v %d", p, out.Int64, kind.String, cost.Float64, ok))
	}
	// 100 in × $2 + 1 out × $10 per million = 0.00021.
	want := []string{"local 1 logprob 0.00021 1", "local 0 logprob 0.5 1", "local 0  0 0"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("rows\n got %q\nwant %q", got, want)
	}
}

func TestUsageSinceIsPerProviderAndPricesTypeSafeAsBefore(t *testing.T) {
	db := newDB(t)
	if _, err := db.Exec(`INSERT INTO jev_usage (profile_id, surface, input_tokens, output_tokens, provider, cost_usd, ok) VALUES
		('p', 'hil', 1000000, NULL, NULL, NULL, 1),
		('p', 'hil', 1000000, 0, 'typesafe', NULL, 0),
		('p', 'hil', 500, 5, 'local', 0.25, 1),
		('p', 'hil', 700, 7, 'local', NULL, 1),
		('p', 'inbox', 10, 1, 'local', NULL, 1)`); err != nil {
		t.Fatal(err)
	}
	u, err := UsageSince(db, "p", testEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if len(u) != 3 {
		t.Fatalf("usage = %+v", u)
	}
	ts, local, inbox := u[1], u[0], u[2]
	if ts.Surface != "hil" || ts.Provider != "typesafe" || ts.Calls != 2 || ts.Failures != 1 || !near(ts.EstimatedUSD, 2*0.042) {
		t.Errorf("typesafe = %+v", ts)
	}
	if local.Provider != "local" || local.Calls != 2 || local.InputTokens != 1200 || local.OutputTokens != 12 || !near(local.EstimatedUSD, 0.25) {
		t.Errorf("local = %+v", local)
	}
	if inbox.Surface != "inbox" || inbox.EstimatedUSD != 0 {
		t.Errorf("inbox = %+v", inbox)
	}
}
