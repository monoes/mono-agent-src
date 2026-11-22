package jevcli

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// fakeKind is the provider kind these tests register (one at a time: no t.Parallel).
const fakeKind = "jevcli-fake"

// probingFake is a backend whose adapter exports Probe, as openaicompat does.
type probingFake struct {
	*jevtest.FakeBackend
	caps  jev.Capabilities
	err   error
	calls int
}

func (p *probingFake) Probe(context.Context) (jev.Capabilities, error) {
	p.calls++
	return p.caps, p.err
}

// registerFake registers fakeKind building b, and records the keys it was built with.
func registerFake(t *testing.T, b jev.Backend) *[]string {
	t.Helper()
	keys := &[]string{}
	t.Cleanup(jevconf.RegisterKind(fakeKind, func(_ jevconf.Provider, key string) (jev.Backend, error) {
		*keys = append(*keys, key)
		return b, nil
	}))
	return keys
}

func jsonMap(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	return m
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var (
	providerFields = []string{"base_url", "builtin", "caps", "key_set", "kind", "model", "name"}
	capsFields     = []string{"kinds", "max_choice_options", "models", "probability", "questions_per_request"}
)

func checkProviderShape(t *testing.T, v any, extra ...string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("provider is %T", v)
	}
	want := append(append([]string{}, providerFields...), extra...)
	sort.Strings(want)
	if got := keysOf(m); !reflect.DeepEqual(got, want) {
		t.Fatalf("provider fields = %v, want %v", got, want)
	}
	caps, _ := m["caps"].(map[string]any)
	if got := keysOf(caps); !reflect.DeepEqual(got, capsFields) {
		t.Fatalf("caps fields = %v, want %v", got, capsFields)
	}
	if _, ok := caps["kinds"].([]any); !ok {
		t.Fatalf("caps.kinds is not an array: %v", caps["kinds"])
	}
	return m
}

// D4: nothing configured lists TypeSafe alone, as the default.
func TestJevProviderListWithNothingConfigured(t *testing.T) {
	cfg := jevTestCfg(t, true)
	out, _, err := runJev(t, cfg, "", "provider", "list")
	if err != nil {
		t.Fatal(err)
	}
	m := jsonMap(t, out)
	if got := keysOf(m); !reflect.DeepEqual(got, []string{"default", "kinds", "providers"}) {
		t.Fatalf("list fields = %v", got)
	}
	ps, _ := m["providers"].([]any)
	if m["default"] != "typesafe" || len(ps) != 1 {
		t.Fatalf("list = %s", out)
	}
	ts := checkProviderShape(t, ps[0])
	if ts["name"] != "typesafe" || ts["builtin"] != true || ts["key_set"] != false {
		t.Fatalf("typesafe = %v", ts)
	}
	if _, ok := m["kinds"].([]any); !ok {
		t.Fatalf("kinds is not an array: %s", out)
	}
	cfg.JSONOutput = false
	if out, _, err = runJev(t, cfg, "", "provider", "list"); err != nil || !strings.Contains(out, "typesafe") || !strings.Contains(out, "native") {
		t.Fatalf("text list: %v\n%s", err, out)
	}
}

func TestJevProviderAddWithKeyFromStdin(t *testing.T) {
	cfg := jevTestCfg(t, true)
	keys := registerFake(t, jevtest.NewFakeBackend(nil))
	const key = "fake-provider-credential-0123"
	out, errOut, err := runJev(t, cfg, key+"\n", "provider", "add", "local", "--kind", fakeKind,
		"--base-url", "http://localhost:1234/v1", "--model", "m1", "--key-stdin")
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut)
	}
	if strings.Contains(out+errOut, key) {
		t.Fatal("the key was printed")
	}
	m := checkProviderShape(t, jsonMap(t, out), "probe")
	if m["name"] != "local" || m["kind"] != fakeKind || m["base_url"] != "http://localhost:1234/v1" || m["model"] != "m1" ||
		m["key_set"] != true || m["builtin"] != false {
		t.Fatalf("added = %s", out)
	}
	probe, _ := m["probe"].(map[string]any)
	if got := keysOf(probe); !reflect.DeepEqual(got, []string{"error", "ok"}) || probe["ok"] != true {
		t.Fatalf("probe = %v", probe)
	}
	if len(*keys) == 0 || (*keys)[0] != key {
		t.Fatalf("the backend was not built with the stored key")
	}
	db := openJevDB(t, cfg)
	p, ok, _ := jevconf.FindProvider(db, "local")
	if !ok || p.SecretName != "decision-local" {
		t.Fatalf("record = %+v", p)
	}
	if _, found, _ := machineEntryID(t.Context(), db, "decision-local"); !found {
		t.Fatal("no machine vault entry decision-local")
	}

	cfg.JSONOutput = false
	out, _, err = runJev(t, cfg, "", "provider", "list")
	if err != nil || !strings.Contains(out, "local") || !strings.Contains(out, "localhost:1234") {
		t.Fatalf("text list: %v\n%s", err, out)
	}
}

func TestJevProviderAddKeylessWithoutProbe(t *testing.T) {
	cfg := jevTestCfg(t, true)
	fb := jevtest.NewFakeBackend(nil)
	registerFake(t, fb)
	out, _, err := runJev(t, cfg, "", "provider", "add", "local", "--kind", fakeKind, "--base-url", "http://127.0.0.1:8080", "--no-probe")
	if err != nil {
		t.Fatal(err)
	}
	m := checkProviderShape(t, jsonMap(t, out))
	if m["key_set"] != false || fb.Calls() != 0 {
		t.Fatalf("keyless add = %s (calls %d)", out, fb.Calls())
	}
	p, _, _ := jevconf.FindProvider(openJevDB(t, cfg), "local")
	if p.SecretName != "" {
		t.Fatalf("keyless record has secret %q", p.SecretName)
	}
	// Refusals are invalid input.
	for _, args := range [][]string{
		{"provider", "add", "x", "--kind", fakeKind},
		{"provider", "add", "remote", "--kind", fakeKind, "--base-url", "http://10.0.0.5/v1", "--no-probe"},
		{"provider", "add", "x", "--kind", "no-such-kind", "--base-url", "https://a.example", "--no-probe"},
		{"provider", "add", "typesafe", "--kind", fakeKind, "--base-url", "https://a.example", "--no-probe"},
	} {
		if _, _, err := runJev(t, cfg, "", args...); clicore.ExitCodeFor(err) != 3 {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// add saves the record even when the provider is down, and says why the probe failed.
func TestJevProviderAddSavesWhenTheProbeFails(t *testing.T) {
	cfg := jevTestCfg(t, true)
	pf := &probingFake{FakeBackend: jevtest.NewFakeBackend(nil), err: errors.New("connection refused")}
	registerFake(t, pf)
	out, _, err := runJev(t, cfg, "", "provider", "add", "local", "--kind", fakeKind, "--base-url", "http://localhost:9")
	if err != nil {
		t.Fatal(err)
	}
	probe, _ := jsonMap(t, out)["probe"].(map[string]any)
	if probe["ok"] != false || !strings.Contains(probe["error"].(string), "connection refused") {
		t.Fatalf("probe = %v", probe)
	}
	if _, ok, _ := jevconf.FindProvider(openJevDB(t, cfg), "local"); !ok {
		t.Fatal("not saved after a failed probe")
	}
}

func TestJevProviderProbeSavesCaps(t *testing.T) {
	cfg := jevTestCfg(t, true)
	caps := jev.Capabilities{Kinds: []string{jev.TypeChoice, jev.TypeNoul}, Probability: jev.ProbSampled, MaxChoiceOptions: 5, QuestionsPerRequest: 1}
	pf := &probingFake{FakeBackend: jevtest.NewFakeBackend(nil), caps: caps}
	registerFake(t, pf)
	if _, _, err := runJev(t, cfg, "", "provider", "add", "local", "--kind", fakeKind, "--base-url", "http://localhost:9", "--no-probe"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runJev(t, cfg, "", "provider", "probe", "local")
	if err != nil {
		t.Fatal(err)
	}
	m := jsonMap(t, out)
	if got := keysOf(m); !reflect.DeepEqual(got, []string{"caps", "error", "latency_ms", "ok"}) || m["ok"] != true || pf.calls != 1 {
		t.Fatalf("probe = %s", out)
	}
	p, _, _ := jevconf.FindProvider(openJevDB(t, cfg), "local")
	if !reflect.DeepEqual(p.Caps, caps) {
		t.Fatalf("saved caps = %+v", p.Caps)
	}
	if _, _, err := runJev(t, cfg, "", "provider", "probe", "nope"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("unknown: %v", err)
	}
	// A failing probe: ok false with exit 0 under --json; an exit-4 error in text.
	pf.err = errors.New("HTTP 503")
	if out, _, err = runJev(t, cfg, "", "provider", "probe", "local"); err != nil || jsonMap(t, out)["ok"] != false {
		t.Fatalf("failed probe json: %v %s", err, out)
	}
	cfg.JSONOutput = false
	if _, _, err = runJev(t, cfg, "", "provider", "probe", "local"); clicore.ExitCodeFor(err) != 4 {
		t.Fatalf("failed probe text: %v", err)
	}
}

func TestJevProviderRemove(t *testing.T) {
	cfg := jevTestCfg(t, true)
	registerFake(t, jevtest.NewFakeBackend(nil))
	if _, _, err := runJev(t, cfg, "k-123\n", "provider", "add", "local", "--kind", fakeKind, "--base-url", "http://localhost:9", "--key-stdin", "--no-probe"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runJev(t, cfg, "", "provider", "use", "local"); err != nil {
		t.Fatal(err)
	}
	_, _, err := runJev(t, cfg, "", "provider", "remove", "local")
	if clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("remove in use: %v", err)
	}
	for _, name := range []string{"typesafe", "nope"} {
		if _, _, err := runJev(t, cfg, "", "provider", "remove", name); clicore.ExitCodeFor(err) != 3 {
			t.Fatalf("remove %s: %v", name, err)
		}
	}
	if _, _, err := runJev(t, cfg, "", "provider", "use", "typesafe", "--yes"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runJev(t, cfg, "", "provider", "remove", "local")
	if err != nil {
		t.Fatal(err)
	}
	if m := jsonMap(t, out); !reflect.DeepEqual(m, map[string]any{"removed": true, "name": "local", "key_removed": true}) {
		t.Fatalf("remove = %s", out)
	}
	db := openJevDB(t, cfg)
	if _, found, _ := machineEntryID(t.Context(), db, "decision-local"); found {
		t.Fatal("the vault entry add created survived")
	}
	// An entry the user named stays.
	if _, _, err := runJev(t, cfg, "k-9\n", "provider", "add", "mine", "--kind", fakeKind, "--base-url", "http://localhost:9",
		"--key-stdin", "--secret-name", "my-key", "--no-probe"); err != nil {
		t.Fatal(err)
	}
	if out, _, err = runJev(t, cfg, "", "provider", "remove", "mine"); err != nil || jsonMap(t, out)["key_removed"] != false {
		t.Fatalf("remove mine: %v %s", err, out)
	}
	if _, found, _ := machineEntryID(t.Context(), db, "my-key"); !found {
		t.Fatal("a named entry was deleted")
	}
}

func TestJevProviderUse(t *testing.T) {
	setJevTTY(t, false)
	cfg := jevTestCfg(t, true)
	registerFake(t, jevtest.NewFakeBackend(nil))
	for _, args := range [][]string{
		{"provider", "add", "local", "--kind", fakeKind, "--base-url", "http://localhost:9", "--no-probe"},
		{"provider", "add", "remote", "--kind", fakeKind, "--base-url", "https://decide.example.com/v1", "--no-probe", "--key-stdin"},
		{"enable", "hil", "--yes"},
	} {
		if _, _, err := runJev(t, cfg, "remote-key-0123456789\n", args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	db := openJevDB(t, cfg)

	// A loopback provider needs no consent.
	out, _, err := runJev(t, cfg, "", "provider", "use", "local")
	if err != nil {
		t.Fatal(err)
	}
	if m := jsonMap(t, out); !reflect.DeepEqual(m, map[string]any{"scope": "default", "surface": "", "provider": "local", "cleared": false}) {
		t.Fatalf("use local = %s", out)
	}

	// D10: another host, with a surface enabled, says what goes where and needs --yes.
	out, errOut, err := runJev(t, cfg, "", "provider", "use", "remote")
	if clicore.ExitCodeFor(err) != 3 || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("use remote without --yes: %v", err)
	}
	if !strings.Contains(errOut, "decide.example.com") || !strings.Contains(errOut, "surface hil") || !strings.Contains(errOut, jevconf.Egress[jevconf.HIL][0]) {
		t.Fatalf("egress not shown:\n%s%s", out, errOut)
	}
	if jevconf.DefaultProvider(db) != "local" {
		t.Fatal("switched without consent")
	}
	if _, _, err = runJev(t, cfg, "", "provider", "use", "remote", "--yes"); err != nil || jevconf.DefaultProvider(db) != "remote" {
		t.Fatalf("use remote --yes: %v", err)
	}
	cfg.JSONOutput = false
	setJevTTY(t, true)
	if out, _, err = runJev(t, cfg, "n\n", "provider", "use", "typesafe"); err != nil || !strings.Contains(out, "Not changed.") || jevconf.DefaultProvider(db) != "remote" {
		t.Fatalf("declined: %v\n%s", err, out)
	}
	if out, _, err = runJev(t, cfg, "y\n", "provider", "use", "typesafe"); err != nil || jevconf.DefaultProvider(db) != "typesafe" {
		t.Fatalf("accepted: %v\n%s", err, out)
	}
	cfg.JSONOutput = true

	// One surface of the profile.
	if out, _, err = runJev(t, cfg, "", "provider", "use", "local", "--surface", "hil"); err != nil {
		t.Fatal(err)
	}
	if m := jsonMap(t, out); m["scope"] != "surface" || m["surface"] != "hil" || m["provider"] != "local" {
		t.Fatalf("use --surface = %s", out)
	}
	if jevconf.ActiveProvider(db, "default", jevconf.HIL) != "local" || jevconf.DefaultProvider(db) != "typesafe" {
		t.Fatal("surface choice not stored")
	}
	// Clearing moves hil's data back to TypeSafe's host: that asks too.
	if _, _, err = runJev(t, cfg, "", "provider", "use", "--surface", "hil", "--clear"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("clear without --yes: %v", err)
	}
	if out, _, err = runJev(t, cfg, "", "provider", "use", "--surface", "hil", "--clear", "--yes"); err != nil {
		t.Fatal(err)
	}
	if m := jsonMap(t, out); !reflect.DeepEqual(m, map[string]any{"scope": "surface", "surface": "hil", "provider": "typesafe", "cleared": true}) {
		t.Fatalf("clear = %s", out)
	}
	if jevconf.ActiveProvider(db, "default", jevconf.HIL) != "typesafe" {
		t.Fatal("clear did not drop the choice")
	}
	for _, args := range [][]string{
		{"provider", "use"}, {"provider", "use", "nope"}, {"provider", "use", "local", "--clear"},
		{"provider", "use", "local", "--surface", "bogus"},
	} {
		if _, _, err := runJev(t, cfg, "", args...); clicore.ExitCodeFor(err) != 3 {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestJevProviderCommandsRefuseWhatIsNotTheOperator(t *testing.T) {
	cfg := jevTestCfg(t, true)
	for _, refusal := range []func() string{nil, func() string { return "an agent runs this" }} {
		for _, args := range [][]string{
			{"provider", "add", "x", "--kind", fakeKind, "--base-url", "http://localhost:9"},
			{"provider", "remove", "x"}, {"provider", "use", "typesafe"},
		} {
			d := cfg.deps()
			d.Refusal = refusal
			cmd := NewCommand(d)
			cmd.SetArgs(args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			var fields interface{ JSONErrorFields() map[string]any }
			if err := cmd.Execute(); !errors.As(err, &fields) || fields.JSONErrorFields()["code"] != "operator_only" {
				t.Errorf("%v: %v", args, err)
			}
		}
	}
}
