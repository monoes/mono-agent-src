package jevcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// How the existing jev commands show and use the decision providers, and
// that they are unchanged with none configured (D4).

func addLocal(t *testing.T, cfg *testConfig, fb jev.Backend) {
	t.Helper()
	registerFake(t, fb)
	if _, _, err := runJev(t, cfg, "", "provider", "add", "local", "--kind", fakeKind, "--base-url", "http://localhost:4321/v1", "--no-probe"); err != nil {
		t.Fatal(err)
	}
}

// D4: with nothing configured, status adds the provider fields at TypeSafe's
// values, no surface names a provider, and the text is as it was.
func TestJevStatusProviderDefaults(t *testing.T) {
	cfg := jevTestCfg(t, true)
	out, _, err := runJev(t, cfg, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	m := jsonMap(t, out)
	if m["provider"] != "typesafe" || m["provider_kind"] != "typesafe" || m["probability"] != "native" ||
		m["provider_host"] != strings.TrimPrefix(strings.TrimRight(jev.DefaultBaseURL, "/"), "https://") {
		t.Fatalf("status = %s", out)
	}
	for _, s := range m["surfaces"].([]any) {
		if _, has := s.(map[string]any)["provider"]; has {
			t.Fatalf("a surface names a provider with none configured: %v", s)
		}
	}
	cfg.JSONOutput = false
	out, _, _ = runJev(t, cfg, "", "status")
	if strings.Contains(strings.ToUpper(out), "PROVIDER") {
		t.Fatalf("the text changed with nothing configured:\n%s", out)
	}
}

func TestJevStatusShowsProviders(t *testing.T) {
	cfg := jevTestCfg(t, true)
	addLocal(t, cfg, jevtest.NewFakeBackend(nil))
	if _, _, err := runJev(t, cfg, "", "provider", "use", "local", "--surface", "inbox"); err != nil {
		t.Fatal(err)
	}
	out, _, _ := runJev(t, cfg, "", "status")
	var st struct {
		Provider string `json:"provider"`
		Surfaces []struct {
			Surface  string `json:"surface"`
			Provider string `json:"provider"`
		} `json:"surfaces"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.Provider != "typesafe" {
		t.Fatalf("status = %s", out)
	}
	for _, s := range st.Surfaces {
		if want := map[bool]string{true: "local", false: ""}[s.Surface == "inbox"]; s.Provider != want {
			t.Fatalf("surface %s provider = %q, want %q", s.Surface, s.Provider, want)
		}
	}
	if _, _, err := runJev(t, cfg, "", "provider", "use", "local"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = runJev(t, cfg, "", "status")
	m := jsonMap(t, out)
	if m["provider"] != "local" || m["provider_kind"] != fakeKind || m["provider_host"] != "localhost:4321" || m["probability"] != "" {
		t.Fatalf("status = %s", out)
	}
	cfg.JSONOutput = false
	out, _, _ = runJev(t, cfg, "", "status")
	if !strings.Contains(out, "Provider: local") || !strings.Contains(out, "localhost:4321") || !strings.Contains(out, "not probed") {
		t.Fatalf("text:\n%s", out)
	}
}

// D10: enable names the host, and --provider sets the surface's provider in the same step.
func TestJevEnableNamesTheHostAndTakesAProvider(t *testing.T) {
	setJevTTY(t, false)
	cfg := jevTestCfg(t, false)
	out, _, err := runJev(t, cfg, "", "enable", "hil", "--yes")
	if err != nil || !strings.Contains(out, "This sends to TypeSafe at "+strings.TrimPrefix(strings.TrimRight(jev.DefaultBaseURL, "/"), "https://")+" (surface hil)") {
		t.Fatalf("typesafe enable: %v\n%s", err, out)
	}
	addLocal(t, cfg, jevtest.NewFakeBackend(nil))
	out, _, err = runJev(t, cfg, "", "enable", "capture", "--provider", "local")
	if clicore.ExitCodeFor(err) != 3 || !strings.Contains(out, `provider "local" at localhost:4321 (surface capture)`) {
		t.Fatalf("enable --provider without --yes: %v\n%s", err, out)
	}
	db := openJevDB(t, cfg)
	if jevconf.ActiveProvider(db, "default", jevconf.Capture) != "typesafe" || jevconf.Enabled(db, "default", jevconf.Capture) {
		t.Fatal("changed without consent")
	}
	cfg.JSONOutput = true
	out, _, err = runJev(t, cfg, "", "enable", "capture", "--provider", "local", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if m := jsonMap(t, out); m["provider"] != "local" || m["host"] != "localhost:4321" || m["enabled"] != true {
		t.Fatalf("enable json = %s", out)
	}
	if jevconf.ActiveProvider(db, "default", jevconf.Capture) != "local" || !jevconf.Enabled(db, "default", jevconf.Capture) {
		t.Fatal("provider or enable not stored")
	}
	// A surface following a non-default provider names that one.
	cfg.JSONOutput = false
	if out, _, _ = runJev(t, cfg, "", "enable", "capture", "--yes"); !strings.Contains(out, `provider "local"`) {
		t.Fatalf("re-enable:\n%s", out)
	}
	if _, _, err = runJev(t, cfg, "", "enable", "inbox", "--provider", "nope", "--yes"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("unknown provider: %v", err)
	}
}

func TestJevUsageByProvider(t *testing.T) {
	cfg := jevTestCfg(t, true)
	db := openJevDB(t, cfg)
	if _, err := db.Exec(`INSERT INTO jev_usage (profile_id, surface, model, questions, input_tokens, output_tokens, latency_ms, ok, provider) VALUES
		('default','hil','jev-latest',1,1000,0,200,1,NULL), ('default','hil','m1',1,300,7,50,1,'local')`); err != nil {
		t.Fatal(err)
	}
	out, _, err := runJev(t, cfg, "", "usage")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Surfaces []map[string]any `json:"surfaces"`
		Total    map[string]any   `json:"total"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Surfaces) != 2 {
		t.Fatalf("usage = %s", out)
	}
	providers := map[string]bool{}
	for _, r := range v.Surfaces {
		if _, ok := r["output_tokens"]; !ok {
			t.Fatalf("row lacks output_tokens: %v", r)
		}
		providers[r["provider"].(string)] = true
	}
	if !providers["typesafe"] || !providers["local"] || v.Total["output_tokens"] != float64(7) {
		t.Fatalf("usage = %s", out)
	}
	cfg.JSONOutput = false
	out, _, _ = runJev(t, cfg, "", "usage")
	if up := strings.ToUpper(out); !strings.Contains(up, "PROVIDER") || !strings.Contains(out, "local") || !strings.Contains(up, "OUTPUT TOKENS") {
		t.Fatalf("text:\n%s", out)
	}
}

// D4: TypeSafe-only usage prints the table it always did.
func TestJevUsageTypeSafeOnlyText(t *testing.T) {
	cfg := jevTestCfg(t, false)
	if _, err := openJevDB(t, cfg).Exec(`INSERT INTO jev_usage (profile_id, surface, model, questions, input_tokens, latency_ms, ok) VALUES
		('default','hil','jev-latest',1,1000,200,1)`); err != nil {
		t.Fatal(err)
	}
	out, _, err := runJev(t, cfg, "", "usage")
	if up := strings.ToUpper(out); err != nil || strings.Contains(up, "PROVIDER") || strings.Contains(up, "OUTPUT TOKENS") || !strings.Contains(out, "1000") {
		t.Fatalf("text: %v\n%s", err, out)
	}
}

func TestJevAskWithAProvider(t *testing.T) {
	cfg := jevTestCfg(t, false)
	fb := jevtest.NewFakeBackend(jevtest.Fixed(map[string]string{"route": "tech"}))
	addLocal(t, cfg, fb)
	req := `{"state":{"text":"it crashes"},"questions":{"route":{"type":"choice","criteria":{"billing":"money","tech":"bugs"}}}}`
	check := func(args ...string) {
		t.Helper()
		out, _, err := runJev(t, cfg, req, append([]string{"ask", "--request", "-"}, args...)...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var resp struct {
			Provider string `json:"provider"`
			Answers  map[string]struct {
				Choice string `json:"choice"`
			} `json:"answers"`
		}
		if err := json.Unmarshal([]byte(out), &resp); err != nil || resp.Answers["route"].Choice != "tech" || resp.Provider != "local" {
			t.Fatalf("%v: %s", args, out)
		}
	}
	check("--provider", "local")
	if _, _, err := runJev(t, cfg, "", "provider", "use", "local"); err != nil {
		t.Fatal(err)
	}
	check() // the machine default
	if fb.Calls() != 2 {
		t.Fatalf("calls = %d", fb.Calls())
	}
	var n int
	_ = openJevDB(t, cfg).QueryRow(`SELECT COUNT(*) FROM jev_usage WHERE surface = 'cli' AND provider = 'local'`).Scan(&n)
	if n != 2 {
		t.Fatalf("usage rows naming the provider = %d", n)
	}
	if _, _, err := runJev(t, cfg, req, "ask", "--request", "-", "--provider", "nope"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("unknown provider: %v", err)
	}
}

// `jev models` and `jev key test` mean TypeSafe's key whatever the default;
// `models --provider` lists another provider's models.
func TestJevModelsAndKeyTestStayTypeSafe(t *testing.T) {
	cfg := jevTestCfg(t, false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"jev-latest"}]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)
	t.Setenv("TYPESAFE_API_KEY", "test-key")
	fb := jevtest.NewFakeBackend(nil)
	addLocal(t, cfg, fb)
	if _, _, err := runJev(t, cfg, "", "provider", "use", "local"); err != nil {
		t.Fatal(err)
	}
	if out, _, err := runJev(t, cfg, "", "models"); err != nil || out != "jev-latest\n" {
		t.Fatalf("models: %v %q", err, out)
	}
	if out, _, err := runJev(t, cfg, "", "key", "test"); err != nil || !strings.Contains(out, "jev-latest") {
		t.Fatalf("key test: %v %q", err, out)
	}
	_, _, err := runJev(t, cfg, "", "models", "--provider", "local")
	if clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "cannot list its models") {
		t.Fatalf("a provider without models: %v", err)
	}
	fb.ModelList = []jev.Model{{ID: "qwen3"}}
	if out, _, err := runJev(t, cfg, "", "models", "--provider", "local"); err != nil || out != "qwen3\n" {
		t.Fatalf("models --provider: %v %q", err, out)
	}
	if _, _, err := runJev(t, cfg, "", "models", "--provider", "nope"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("unknown provider: %v", err)
	}
}
