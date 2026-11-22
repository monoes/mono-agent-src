package jevcli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

const remoteKey = "remote-key-0123456789"

func addRemote(t *testing.T, cfg *testConfig, name, base string) {
	t.Helper()
	if _, errOut, err := runJev(t, cfg, remoteKey+"\n", "provider", "add", name, "--kind", fakeKind, "--base-url", base,
		"--no-probe", "--key-stdin"); err != nil {
		t.Fatalf("add %s: %v\n%s", name, err, errOut)
	}
}

// S2: making a provider on another host the default always asks, even when
// no surface is enabled, and names the jev org deciders that follow the
// default and the workflow nodes that do. A decider with its own provider,
// one of another kind, or of a profile that chose its decider surface's
// provider, is not listed. The JSON result keeps its shape.
func TestDefaultSwitchConsentCoversDecidersAndNodes(t *testing.T) {
	setJevTTY(t, false)
	cfg := jevTestCfg(t, true)
	registerFake(t, jevtest.NewFakeBackend(nil))
	addRemote(t, cfg, "remote", "https://decide.example.com/v1")
	addRemote(t, cfg, "other", "https://other.example.com/v1")
	db := openJevDB(t, cfg)
	if _, err := db.Exec(`INSERT INTO org_autonomy (profile_id, org_name, level, decider_json, updated_at, updated_by) VALUES
		('default', 'acme', 'full', '{"kind":"jev"}', 'x', 'cli'),
		('default', 'beta', 'full', '{"kind":"jev","provider":"other"}', 'x', 'cli'),
		('default', 'gamma', 'full', '{"kind":"model"}', 'x', 'cli'),
		('p2', 'delta', 'full', '{"kind":"jev"}', 'x', 'cli')`); err != nil {
		t.Fatal(err)
	}
	if err := jevconf.SetSurfaceProvider(t.Context(), db, "p2", jevconf.Decider, "other"); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runJev(t, cfg, "", "provider", "use", "remote")
	if clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("a remote default without --yes: %v", err)
	}
	for _, want := range []string{"decide.example.com", "org decider of default/acme", "every workflow node that uses a decision model without naming a provider", "dynorg"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("consent lacks %q:\n%s", want, errOut)
		}
	}
	for _, not := range []string{"beta", "gamma", "delta"} {
		if strings.Contains(errOut, not) {
			t.Errorf("consent lists %s:\n%s", not, errOut)
		}
	}
	if jevconf.DefaultProvider(db) != jev.ProviderTypeSafe {
		t.Fatal("switched without consent")
	}
	out, _, err := runJev(t, cfg, "", "provider", "use", "remote", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if m := jsonMap(t, out); !reflect.DeepEqual(m, map[string]any{"scope": "default", "surface": "", "provider": "remote", "cleared": false}) {
		t.Fatalf("use remote = %s", out)
	}
	// Back to TypeSafe (another host as well) asks too.
	if _, _, err := runJev(t, cfg, "", "provider", "use", "typesafe"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("back to TypeSafe without --yes: %v", err)
	}
}

// S3: `provider add` over an existing name that moves it to another host
// says old → new and asks; confirmed, the stored key's binding follows the
// host. A change of model only does not ask.
func TestAddOverAnExistingNameWithANewOriginAsks(t *testing.T) {
	setJevTTY(t, false)
	cfg := jevTestCfg(t, true)
	keys := registerFake(t, jevtest.NewFakeBackend(nil))
	addRemote(t, cfg, "router", "https://old.example.com/v1")
	db := openJevDB(t, cfg)
	args := []string{"provider", "add", "router", "--kind", fakeKind, "--base-url", "https://new.example.com/v1", "--no-probe",
		"--secret-name", "decision-router"}
	_, errOut, err := runJev(t, cfg, "", args...)
	if clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("a new host without --yes: %v", err)
	}
	if !strings.Contains(errOut, "old.example.com to new.example.com") || !strings.Contains(errOut, `vault entry "decision-router"`) {
		t.Fatalf("consent:\n%s", errOut)
	}
	if p, _, _ := jevconf.FindProvider(db, "router"); p.BaseURL != "https://old.example.com/v1" {
		t.Fatalf("replaced without consent: %+v", p)
	}
	out, _, err := runJev(t, cfg, "", append(args, "--yes")...)
	if err != nil {
		t.Fatal(err)
	}
	checkProviderShape(t, jsonMap(t, out))
	// The key's binding moved with the confirmed host: the client builds.
	if _, _, err := jevconf.ResolveProviderClient(t.Context(), db, "default", "router", "", ""); err != nil {
		t.Fatalf("after a confirmed move: %v", err)
	}
	if n := len(*keys); n == 0 || (*keys)[n-1] != remoteKey {
		t.Fatalf("keys = %v", *keys)
	}
	// The model alone: no question, also for a provider in use.
	if err := jevconf.SetDefaultProvider(t.Context(), db, "router"); err != nil {
		t.Fatal(err)
	}
	if _, errOut, err := runJev(t, cfg, "", "provider", "add", "router", "--kind", fakeKind, "--base-url", "https://new.example.com/v1",
		"--no-probe", "--secret-name", "decision-router", "--model", "m2"); err != nil || errOut != "" {
		t.Fatalf("a model change: %v\n%s", err, errOut)
	}
	// In use, its key entry changes: that asks.
	if _, _, err := runJev(t, cfg, "", "provider", "add", "router", "--kind", fakeKind, "--base-url", "https://new.example.com/v1",
		"--no-probe", "--secret-name", "elsewhere"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("a new key entry for a provider in use: %v", err)
	}
}

// C6: a provider whose probed kinds lack one a surface asks is refused for it.
func TestSelectingAProviderChecksTheQuestionKinds(t *testing.T) {
	setJevTTY(t, false)
	cfg := jevTestCfg(t, true)
	registerFake(t, jevtest.NewFakeBackend(nil))
	if _, _, err := runJev(t, cfg, "", "provider", "add", "nli", "--kind", fakeKind, "--base-url", "http://localhost:9", "--no-probe"); err != nil {
		t.Fatal(err)
	}
	db := openJevDB(t, cfg)
	p, _, _ := jevconf.FindProvider(db, "nli")
	p.Caps = jev.Capabilities{Kinds: []string{jev.TypeChoice}, Probability: jev.ProbLogprob}
	if err := jevconf.SaveProvider(t.Context(), db, p); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"provider", "use", "nli", "--surface", "people_links"},
		{"enable", "hil", "--provider", "nli", "--yes"},
	} {
		if _, _, err := runJev(t, cfg, "", args...); clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "does not answer") {
			t.Errorf("%v: %v", args, err)
		}
	}
	if jevconf.Enabled(db, "default", jevconf.HIL) {
		t.Fatal("hil was enabled on a provider that cannot answer it")
	}
	if _, _, err := runJev(t, cfg, "", "enable", "capture", "--provider", "nli", "--yes"); err != nil {
		t.Fatalf("capture asks choice only: %v", err)
	}
}

// C10: a surface's own provider shows even when it names the default, with
// provider_pinned; a default provider missing from the records says so.
func TestStatusShowsPinnedSurfacesAndAnUnknownDefault(t *testing.T) {
	cfg := jevTestCfg(t, true)
	db := openJevDB(t, cfg)
	if err := jevconf.SetSurfaceProvider(t.Context(), db, "default", jevconf.HIL, "typesafe"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runJev(t, cfg, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	surfaces, _ := jsonMap(t, out)["surfaces"].([]any)
	for _, s := range surfaces {
		m := s.(map[string]any)
		pinned := m["surface"] == "hil"
		if (m["provider_pinned"] == true) != pinned || (m["provider"] == "typesafe") != pinned {
			t.Errorf("surface %v: %v", m["surface"], m)
		}
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('jev.provider', 'gone')`); err != nil {
		t.Fatal(err)
	}
	cfg.JSONOutput = false
	if out, _, err = runJev(t, cfg, "", "status"); err != nil || !strings.Contains(out, "Provider: gone (unknown provider") {
		t.Fatalf("unknown default: %v\n%s", err, out)
	}
}

// C8: `jev models` with nothing configured fails as before providers: no
// key is an auth failure (exit 4).
func TestJevModelsKeepsItsExitCodes(t *testing.T) {
	cfg := jevTestCfg(t, true)
	if _, _, err := runJev(t, cfg, "", "models"); clicore.ExitCodeFor(err) != 4 {
		t.Fatalf("no key: %v (exit %d)", err, clicore.ExitCodeFor(err))
	}
}
