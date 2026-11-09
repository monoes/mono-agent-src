package openaiapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testdb"
)

// reportModels is one model of each class, and an alias, which a report leaves out.
var reportModels = []ModelInfo{
	{ID: "claude/default", Runtime: "claude", Model: "default", Label: "Default", Class: ChatOnly, Validated: true, Sandboxable: true},
	{ID: "claude/opus", Runtime: "claude", Model: "opus", Label: "Opus", Class: ChatOnly, Alias: true, Sandboxable: true},
	{ID: "codex/gpt-6-astra", Runtime: "codex", Model: "gpt-6-astra", Label: "GPT-6-Astra", Class: Sandboxed, Sandboxable: true},
	{ID: "antigravity/default", Runtime: "antigravity", Model: "default", Label: "antigravity default model", Class: Unconfined, Sandboxable: true},
}

func reportOf(kind string, p Policy, auto AutoStatus, models []ModelInfo) ModelsReport {
	return NewModelsReport(ModelsReportInput{For: kind, Policy: p, Source: ReportSourceShell, Models: models, Auto: auto})
}

type marks struct{ allowed, context, auto bool }

func marksOf(r ModelsReport) map[string]marks {
	out := map[string]marks{}
	for _, m := range r.Models {
		out[m.ID] = marks{m.Allowed, m.ContextAllowed, m.AutoAllowed}
	}
	return out
}

func TestModelsReportMarksWhatEachPolicyAllows(t *testing.T) {
	for _, c := range []struct {
		name   string
		kind   string
		policy Policy
		want   map[string]marks
		caps   [3]string // policy.confinement, context_confinement, auto_confinement
	}{
		{
			// A loopback listener serves everything, but a context key and auto stay
			// among chat-only models until the operator raises them.
			name: "loopback, nothing raised", kind: "loopback", policy: Policy{Max: Unconfined},
			want: map[string]marks{
				"claude/default":      {true, true, true},
				"codex/gpt-6-astra":   {true, false, false},
				"antigravity/default": {true, false, false},
			},
			caps: [3]string{"any", "chat-only", "chat-only"},
		},
		{
			name: "loopback, both raised", kind: "loopback", policy: Policy{Max: Unconfined, ContextMax: Sandboxed, AutoMax: Unconfined},
			want: map[string]marks{
				"claude/default":      {true, true, true},
				"codex/gpt-6-astra":   {true, true, true},
				"antigravity/default": {true, false, true},
			},
			caps: [3]string{"any", "sandboxed", "any"},
		},
		{
			// Neither cap is ever above what the listener serves.
			name: "network listener, caps above it", kind: "network", policy: Policy{Max: ChatOnly, ContextMax: Unconfined, AutoMax: Unconfined},
			want: map[string]marks{
				"claude/default":      {true, true, true},
				"codex/gpt-6-astra":   {false, false, false},
				"antigravity/default": {false, false, false},
			},
			caps: [3]string{"chat-only", "chat-only", "chat-only"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := reportOf(c.kind, c.policy, AutoStatus{}, reportModels)
			if got := marksOf(r); !reflect.DeepEqual(got, c.want) {
				t.Errorf("rows %v, want %v", got, c.want)
			}
			if len(r.Models) != 3 {
				t.Errorf("%d rows: an alias is not a model of its own and is left out", len(r.Models))
			}
			if r.V != 1 || r.Policy.For != c.kind || r.Policy.Source != ReportSourceShell ||
				[3]string{r.Policy.Confinement, r.Policy.ContextConfinement, r.Policy.AutoConfinement} != c.caps {
				t.Errorf("policy %+v, want for=%s confinement/context/auto=%v", r.Policy, c.kind, c.caps)
			}
			// Each row says what the model is.
			if m := r.Models[0]; m.ID != "claude/default" || m.Runtime != "claude" || m.Model != "default" || m.Label != "Default" ||
				m.Confinement != "chat-only" || !m.Validated {
				t.Errorf("row %+v", m)
			}
		})
	}
}

func TestModelsReportSaysWhetherAutoWorks(t *testing.T) {
	codexOnly := []ModelInfo{reportModels[2]}
	vault := AutoStatus{Available: true, KeySource: "vault"}
	for _, c := range []struct {
		name   string
		policy Policy
		auto   AutoStatus
		models []ModelInfo
		want   AutoReport
	}{
		{"off clears what only an available auto has", Policy{Max: Unconfined}, AutoStatus{Missing: "the surface", KeySource: "env"}, reportModels,
			AutoReport{Missing: "the surface"}},
		{"on: chat-only unless raised", Policy{Max: Unconfined}, vault, reportModels,
			AutoReport{Available: true, KeySource: "vault", Confinement: "chat-only", Candidates: 1, HeldBack: 2}},
		{"raised to sandboxed", Policy{Max: Unconfined, AutoMax: Sandboxed}, vault, reportModels,
			AutoReport{Available: true, KeySource: "vault", Confinement: "sandboxed", Candidates: 2, HeldBack: 1}},
		{"raised to any holds nothing back", Policy{Max: Unconfined, AutoMax: Unconfined}, vault, reportModels,
			AutoReport{Available: true, KeySource: "vault", Confinement: "unconfined", Candidates: 3}},
		{"a network listener serves chat-only, whatever auto is given", Policy{Max: ChatOnly, AutoMax: Unconfined}, vault, reportModels,
			AutoReport{Available: true, KeySource: "vault", Confinement: "chat-only", Candidates: 1}},
		{"nothing installed", Policy{Max: Unconfined}, vault, nil,
			AutoReport{Missing: "at least one model the listener's policy allows"}},
		{"the listener serves none of them", Policy{Max: ChatOnly}, vault, codexOnly,
			AutoReport{Missing: "at least one model the listener's policy allows"}},
		{"auto may pick none of what the listener serves", Policy{Max: Unconfined}, vault, codexOnly,
			AutoReport{Missing: "a model within --auto-confinement (chat-only), which holds back all 1 the listener serves"}},
		{"raised, the same models are enough", Policy{Max: Unconfined, AutoMax: Sandboxed}, vault, codexOnly,
			AutoReport{Available: true, KeySource: "vault", Confinement: "sandboxed", Candidates: 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := reportOf("loopback", c.policy, c.auto, c.models).Auto; got != c.want {
				t.Errorf("auto %+v, want %+v", got, c.want)
			}
		})
	}
}

// What a client of `api models --json` reads: the order of the fields, the ones
// left out when empty, and a list that is [] and never null.
func TestModelsReportJSONShape(t *testing.T) {
	got, err := json.Marshal(reportOf("loopback", Policy{Max: Unconfined}, AutoStatus{Available: true, KeySource: "env"}, reportModels[:1]))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"v":1,"policy":{"for":"loopback","confinement":"any","context_confinement":"chat-only","auto_confinement":"chat-only","source":"shell"},` +
		`"models":[{"id":"claude/default","runtime":"claude","model":"default","label":"Default","confinement":"chat-only","validated":true,"allowed":true,"context_allowed":true,"auto_allowed":true,"capabilities":["text","tools"]}],` +
		`"auto":{"available":true,"key_source":"env","confinement":"chat-only","candidates":1}}`
	if string(got) != want {
		t.Errorf("json\n got %s\nwant %s", got, want)
	}

	got, _ = json.Marshal(reportOf("network", Policy{Max: ChatOnly}, AutoStatus{Missing: "a key"}, nil))
	want = `{"v":1,"policy":{"for":"network","confinement":"chat-only","context_confinement":"chat-only","auto_confinement":"chat-only","source":"shell"},` +
		`"models":[],"auto":{"available":false,"missing":"a key"}}`
	if string(got) != want {
		t.Errorf("json\n got %s\nwant %s", got, want)
	}
}

// A report says which models make images, as GET /v1/models does: the models of the
// runtimes of the image list, as far as they can write a file. The report lists every
// model, whether or not the policy serves it, and so does this.
func TestModelsReportSaysWhichModelsMakeImages(t *testing.T) {
	// claude calls tools whatever the image list says: that is another list.
	claude, text, both := []string{"text", "tools"}, []string{"text"}, []string{"text", "image"}
	for _, c := range []struct {
		name string
		list []string
		want map[string][]string
	}{
		{"the default list", nil, map[string][]string{"claude/default": claude, "codex/gpt-6-astra": both, "antigravity/default": both}},
		{"a list of one", []string{"antigravity"}, map[string][]string{"claude/default": claude, "codex/gpt-6-astra": text, "antigravity/default": both}},
		{"a chat-only runtime in the list cannot", []string{"claude", "codex"}, map[string][]string{"claude/default": claude, "codex/gpt-6-astra": both, "antigravity/default": text}},
		// A list with nothing in it is image generation switched off, which is not the default list.
		{"switched off", []string{}, map[string][]string{"claude/default": claude, "codex/gpt-6-astra": text, "antigravity/default": text}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := NewModelsReport(ModelsReportInput{For: "loopback", Policy: Policy{Max: ChatOnly}, Source: ReportSourceShell, Models: reportModels, ImageRuntimes: c.list})
			got := map[string][]string{}
			for _, m := range r.Models {
				got[m.ID] = m.Capabilities
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("capabilities %v, want %v", got, c.want)
			}
		})
	}
}

// The image list of a report comes from the same variable the gateway reads: unset is
// the default list, "none" a list with nothing in it (not nil, which would be the
// default), and a bad value an error that names the variable.
func TestEffectiveImageRuntimes(t *testing.T) {
	for _, c := range []struct {
		value string
		want  []string
	}{
		{"", []string{"codex", "antigravity"}},
		{"agy, codex", []string{"antigravity", "codex"}},
		{"none", []string{}},
	} {
		got, err := EffectiveImageRuntimes(envOf(map[string]string{"MONOAGENT_API_IMAGE_RUNTIMES": c.value}))
		if err != nil || got == nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("EffectiveImageRuntimes(%q) = %#v, %v; want %#v", c.value, got, err, c.want)
		}
	}
	got, err := EffectiveImageRuntimes(envOf(nil))
	if err != nil || !reflect.DeepEqual(got, []string{"codex", "antigravity"}) {
		t.Errorf("EffectiveImageRuntimes with the variable unset = %#v, %v; want the default list", got, err)
	}
	if _, err := EffectiveImageRuntimes(envOf(map[string]string{"MONOAGENT_API_IMAGE_RUNTIMES": "co dex"})); err == nil || !strings.Contains(err.Error(), "MONOAGENT_API_IMAGE_RUNTIMES") {
		t.Errorf("a bad value must be an error that names the variable, got %v", err)
	}
}

func TestModelsReportSaysWhoseSettingsItEvaluates(t *testing.T) {
	r := NewModelsReport(ModelsReportInput{For: "loopback", Policy: Policy{Max: Unconfined}, Source: ReportSourceMCP})
	if r.Policy.Source != "mcp" {
		t.Errorf("source %q, want mcp", r.Policy.Source)
	}
	if ReportSourceShell != "shell" {
		t.Errorf("the CLI's source is %q, which clients of `api models --json` read as shell", ReportSourceShell)
	}
}

func TestListenerAddr(t *testing.T) {
	for kind, want := range map[string]string{"loopback": "127.0.0.1:0", "network": "0.0.0.0:0"} {
		if got, err := ListenerAddr(kind); err != nil || got != want {
			t.Errorf("ListenerAddr(%q) = %q, %v; want %q", kind, got, err, want)
		}
	}
	for _, bad := range []string{"", "moon", "Loopback"} {
		_, err := ListenerAddr(bad)
		if err == nil || !strings.Contains(err.Error(), "loopback or network") || !strings.Contains(err.Error(), `"`+bad+`"`) {
			t.Errorf("ListenerAddr(%q) error %v, want it to name the two kinds and the value", bad, err)
		}
	}
}

func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestEffectivePolicyPrecedence(t *testing.T) {
	for _, c := range []struct {
		addr, flag, env, want string
	}{
		{"127.0.0.1:1", "", "", "any"},
		{"0.0.0.0:1", "", "", "chat-only"},
		{"127.0.0.1:1", "", "chat-only", "chat-only"},
		{"0.0.0.0:1", "any", "chat-only", "any"}, // the flag beats the environment
	} {
		got, err := EffectivePolicy(c.addr, c.flag, envOf(map[string]string{"MONOAGENT_API_CONFINEMENT": c.env}))
		if err != nil || got.String() != c.want {
			t.Errorf("EffectivePolicy(%q, %q, env=%q) = %s, %v; want %s", c.addr, c.flag, c.env, got, err, c.want)
		}
	}
	for _, bad := range []struct{ flag, env string }{{"nope", ""}, {"", "nope"}} {
		_, err := EffectivePolicy("127.0.0.1:1", bad.flag, envOf(map[string]string{"MONOAGENT_API_CONFINEMENT": bad.env}))
		if err == nil || !strings.Contains(err.Error(), "unknown confinement") {
			t.Errorf("a bad value %+v must be an error that says so, got %v", bad, err)
		}
	}
}

// The context cap and the auto cap each read their own variable, flag first,
// chat-only when neither is set.
func TestEffectiveCapsPrecedence(t *testing.T) {
	for _, c := range []struct {
		name    string
		fn      func(string, func(string) string) (Class, error)
		varName string
		other   string
	}{
		{"context", EffectiveContextMax, "MONOAGENT_API_CONTEXT_CONFINEMENT", "MONOAGENT_API_AUTO_CONFINEMENT"},
		{"auto", EffectiveAutoMax, "MONOAGENT_API_AUTO_CONFINEMENT", "MONOAGENT_API_CONTEXT_CONFINEMENT"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, k := range []struct {
				flag string
				env  map[string]string
				want Class
			}{
				{"", nil, ChatOnly}, // nothing set: chat-only
				{"", map[string]string{c.varName: "sandboxed"}, Sandboxed},
				{"any", map[string]string{c.varName: "chat-only"}, Unconfined}, // the flag beats the environment
				{"", map[string]string{c.other: "any"}, ChatOnly},              // the other cap's variable is not this one's
			} {
				got, err := c.fn(k.flag, envOf(k.env))
				if err != nil || got != k.want {
					t.Errorf("flag %q env %v: %v, %v; want %v", k.flag, k.env, got, err, k.want)
				}
			}
			for _, bad := range []struct {
				flag string
				env  map[string]string
			}{{"nope", nil}, {"", map[string]string{c.varName: "nope"}}} {
				if _, err := c.fn(bad.flag, envOf(bad.env)); err == nil || !strings.Contains(err.Error(), "unknown confinement") {
					t.Errorf("a bad value %+v must be an error that says so, got %v", bad, err)
				}
			}
		})
	}
}

// useFakeMonomind points monomind at the fake binary of the end-to-end test, which
// lists claude and two of its models.
func useFakeMonomind(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake monomind is a shell script")
	}
	testhome.SetHome(t, t.TempDir())
	bin := filepath.Join(t.TempDir(), "monomind")
	if err := os.WriteFile(bin, []byte(fakeMonomind), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
}

// LoadModels is the gateway's own catalog over the installed runtimes: here the
// fake monomind of the end-to-end test, which has claude and two of its models.
func TestLoadModelsListsWhatTheInstalledRuntimesOffer(t *testing.T) {
	useFakeMonomind(t)

	models, err := LoadModels(context.Background(), testdb.Open(t).DB)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(models); !reflect.DeepEqual(got, []string{"claude/default", "claude/sonnet"}) {
		t.Errorf("models %v", got)
	}
	for _, m := range models {
		if m.Class != ChatOnly {
			t.Errorf("%s is %v, want chat-only: monomind's native_sandbox for claude", m.ID, m.Class)
		}
	}
}

// LoadModels is a one-shot call: its load ends with the caller that made it, where
// the gateway's outlives its first request.
func TestLoadModelsEndsWithItsCaller(t *testing.T) {
	useFakeMonomind(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadModels(ctx, testdb.Open(t).DB); !errors.Is(err, context.Canceled) {
		t.Errorf("LoadModels with a context that has ended: err = %v, want context.Canceled", err)
	}
}

// A catalog that is kept (an MCP server keeps one) lists what the runtimes offer,
// and answers the next call from what it loaded: nothing is started again within
// the TTL, which the missing binary shows.
func TestNewModelCatalogKeepsWhatItLoaded(t *testing.T) {
	useFakeMonomind(t)
	c := NewModelCatalog(testdb.Open(t).DB, time.Minute)

	first, err := c.ModelsBound(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(first); !reflect.DeepEqual(got, []string{"claude/default", "claude/sonnet"}) {
		t.Fatalf("models %v", got)
	}
	t.Setenv(monomind.EnvOverride, filepath.Join(t.TempDir(), "gone"))
	monomind.ResetCapabilityCache()
	again, err := c.ModelsBound(context.Background())
	if err != nil || !reflect.DeepEqual(ids(again), ids(first)) {
		t.Errorf("the second call within the TTL: %v, %v; want the list it already had", ids(again), err)
	}
}
