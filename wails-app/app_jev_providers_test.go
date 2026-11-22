package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeProviderKey = "fake_provider_SECRET_456"

const fakeProviderList = `{"default":"typesafe","providers":[` +
	`{"name":"typesafe","kind":"typesafe","base_url":"https://api.typesafe.ai","model":"jev-latest","key_set":true,"builtin":true,` +
	`"caps":{"kinds":["choice","noul","score"],"probability":"native","max_choice_options":255,"questions_per_request":32,"models":true}},` +
	`{"name":"local","kind":"openai","base_url":"http://localhost:11434/v1","model":"llama3.1","key_set":false,"builtin":false,` +
	`"caps":{"kinds":["choice","noul"],"probability":"logprob","max_choice_options":20,"questions_per_request":1,"models":true}}],` +
	`"kinds":["openai","anthropic","hf-zeroshot"]}`

// fakeProviderCLI answers the `jev provider …` verbs with canned JSON, logs
// argv one line per call and, for `add`, the stdin it received.
func fakeProviderCLI(t *testing.T) (argsLog, stdinLog string) {
	t.Helper()
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	stdinLog = filepath.Join(dir, "stdin.log")
	script := `#!/bin/sh
echo "$*" >> '` + argsLog + `'
case "$*" in
  *" jev provider list"*) echo '` + fakeProviderList + `';;
  *" jev provider add "*) cat >> '` + stdinLog + `'; echo '{"name":"router","kind":"openai","base_url":"https://openrouter.ai/api/v1","model":"m","key_set":true,"builtin":false,"caps":{"kinds":["choice"],"probability":"logprob","max_choice_options":20,"questions_per_request":1,"models":true},"probe":{"ok":false,"error":"HTTP 401"}}';;
  *" jev provider use "*"--clear"*) echo '{"scope":"surface","surface":"hil","provider":"typesafe","cleared":true}';;
  *" jev provider use "*"--surface"*) echo '{"scope":"surface","surface":"hil","provider":"local","cleared":false}';;
  *" jev provider use "*) echo '{"scope":"default","surface":"","provider":"local","cleared":false}';;
  *" jev provider probe "*) echo '{"ok":true,"caps":{"kinds":["choice"],"probability":"logprob","max_choice_options":20,"questions_per_request":1,"models":true},"latency_ms":42,"error":""}';;
  *" jev provider remove "*) echo '{"removed":true,"name":"local","key_removed":false}';;
  *" jev status"*) echo '{"profile_id":"work","key_source":"vault","key_entry":"typesafe","model":"jev-latest","base_url":"https://api.typesafe.ai","provider":"local","provider_kind":"openai","provider_host":"localhost:11434","probability":"logprob","surfaces":[{"surface":"hil","title":"t","description":"d","egress":["a"],"enabled":true,"threshold":0.9,"default_threshold":0.9,"provider":"local"}]}';;
  *" jev usage"*) echo '{"profile_id":"work","since":"x","surfaces":[{"surface":"hil","provider":"local","calls":2,"failures":0,"input_tokens":100,"output_tokens":2,"estimated_usd":0,"avg_latency_ms":30}],"total":{"surface":"total","calls":2,"input_tokens":100,"output_tokens":2}}';;
  *) echo 'unexpected' >&2; exit 2;;
esac
`
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	return argsLog, stdinLog
}

func providerTestApp(t *testing.T) *App {
	t.Helper()
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")
	return a
}

func TestJevProviderFuncsShellOutToCLI(t *testing.T) {
	argsLog, stdinLog := fakeProviderCLI(t)
	a := providerTestApp(t)

	list, err := a.JevProviders()
	if err != nil || list.Default != "typesafe" || len(list.Providers) != 2 || len(list.Kinds) != 3 {
		t.Fatalf("JevProviders = %+v, %v", list, err)
	}
	if p := list.Providers[1]; p.Name != "local" || p.KeySet || p.Builtin || p.Caps.Probability != "logprob" ||
		p.Caps.QuestionsPerRequest != 1 || !p.Caps.Models || p.Caps.MaxChoiceOptions != 20 {
		t.Fatalf("local provider = %+v", p)
	}

	added, err := a.JevAddProvider(JevAddProviderRequest{
		Name: "router", Kind: "openai", BaseURL: "https://openrouter.ai/api/v1", Model: "m",
		APIKey: "  " + fakeProviderKey + "\n", USDPerMTokIn: 0.15, USDPerMTokOut: 0.6,
	})
	if err != nil || added.Name != "router" || !added.KeySet || added.Probe == nil || added.Probe.OK || added.Probe.Error != "HTTP 401" {
		t.Fatalf("JevAddProvider = %+v, %v", added, err)
	}
	// Keyless (a local server), no prices, no probe.
	if _, err := a.JevAddProvider(JevAddProviderRequest{Name: "ollama", Kind: "openai", BaseURL: "http://localhost:11434/v1", NoProbe: true}); err != nil {
		t.Fatal(err)
	}

	if u, err := a.JevUseProvider("local", ""); err != nil || u.Scope != "default" || u.Provider != "local" {
		t.Fatalf("use default = %+v, %v", u, err)
	}
	if u, err := a.JevUseProvider("local", "hil"); err != nil || u.Scope != "surface" || u.Surface != "hil" {
		t.Fatalf("use surface = %+v, %v", u, err)
	}
	if u, err := a.JevUseProvider("", "hil"); err != nil || !u.Cleared || u.Provider != "typesafe" {
		t.Fatalf("clear surface = %+v, %v", u, err)
	}
	if pr, err := a.JevProbeProvider("local"); err != nil || !pr.OK || pr.LatencyMS != 42 || pr.Caps.Probability != "logprob" {
		t.Fatalf("probe = %+v, %v", pr, err)
	}
	if rm, err := a.JevRemoveProvider("local"); err != nil || !rm.Removed || rm.Name != "local" || rm.KeyRemoved {
		t.Fatalf("remove = %+v, %v", rm, err)
	}

	want := []string{
		"--profile work --json jev provider list",
		"--profile work --json jev provider list",
		"--profile work --json jev provider add router --kind openai --base-url=https://openrouter.ai/api/v1 --model=m --key-stdin --usd-per-mtok-in 0.15 --usd-per-mtok-out 0.6",
		"--profile work --json jev provider list",
		"--profile work --json jev provider add ollama --kind openai --base-url=http://localhost:11434/v1 --no-probe",
		"--profile work --json jev provider use local --yes",
		"--profile work --json jev provider use local --surface hil --yes",
		"--profile work --json jev provider use --surface hil --clear --yes",
		"--profile work --json jev provider probe local",
		"--profile work --json jev provider remove local",
	}
	got := loggedArgs(t, argsLog)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The key reaches the CLI on stdin, trimmed, and never on argv. The
	// keyless add sends nothing on stdin, so the log holds the one key only.
	stdin, err := os.ReadFile(stdinLog)
	if err != nil || string(stdin) != fakeProviderKey {
		t.Fatalf("stdin = %q, %v", stdin, err)
	}
	if raw, _ := os.ReadFile(argsLog); strings.Contains(string(raw), "SECRET") {
		t.Fatal("the key appeared in argv")
	}
}

// The status and usage gain the provider fields; an older CLI that prints
// none of them still reads (TestJevFuncsShellOutToCLI covers that shape).
func TestJevStatusAndUsageCarryProvider(t *testing.T) {
	fakeProviderCLI(t)
	a := providerTestApp(t)
	st, err := a.JevStatus()
	if err != nil || st.Provider != "local" || st.ProviderKind != "openai" || st.ProviderHost != "localhost:11434" ||
		st.Probability != "logprob" || st.Surfaces[0].Provider != "local" {
		t.Fatalf("JevStatus = %+v, %v", st, err)
	}
	u, err := a.JevUsage("7d")
	if err != nil || u.Surfaces[0].Provider != "local" || u.Surfaces[0].OutputTokens != 2 || u.Total.OutputTokens != 2 {
		t.Fatalf("JevUsage = %+v, %v", u, err)
	}
}

func TestJevProviderRefusesBadInputBeforeCLI(t *testing.T) {
	argsLog, _ := fakeProviderCLI(t)
	a := providerTestApp(t)
	for _, name := range []string{"", "-x", "--yes", "Local", "a b", "a/b", strings.Repeat("a", 33), "a;b", "über"} {
		if _, err := a.JevUseProvider(name, ""); err == nil {
			t.Errorf("use %q must be refused", name)
		}
		if _, err := a.JevProbeProvider(name); err == nil {
			t.Errorf("probe %q must be refused", name)
		}
		if _, err := a.JevRemoveProvider(name); err == nil {
			t.Errorf("remove %q must be refused", name)
		}
	}
	for _, s := range []string{"--clear", "-s", "HIL", "a b"} {
		if _, err := a.JevUseProvider("local", s); err == nil {
			t.Errorf("surface %q must be refused", s)
		}
	}
	ok := JevAddProviderRequest{Name: "router", Kind: "openai", BaseURL: "https://openrouter.ai/api/v1", Model: "m"}
	bad := map[string]func(r *JevAddProviderRequest){
		"name":         func(r *JevAddProviderRequest) { r.Name = "-n" },
		"url empty":    func(r *JevAddProviderRequest) { r.BaseURL = "" },
		"url scheme":   func(r *JevAddProviderRequest) { r.BaseURL = "file:///etc/passwd" },
		"url no host":  func(r *JevAddProviderRequest) { r.BaseURL = "https://" },
		"url flag":     func(r *JevAddProviderRequest) { r.BaseURL = "--kind=x" },
		"url creds":    func(r *JevAddProviderRequest) { r.BaseURL = "https://u:p@openrouter.ai/api/v1" },
		"model flag":   func(r *JevAddProviderRequest) { r.Model = "-m" },
		"model space":  func(r *JevAddProviderRequest) { r.Model = "a b" },
		"price":        func(r *JevAddProviderRequest) { r.USDPerMTokIn = -1 },
		"price out":    func(r *JevAddProviderRequest) { r.USDPerMTokOut = -0.5 },
		"kind empty":   func(r *JevAddProviderRequest) { r.Kind = "" },
		"kind flag":    func(r *JevAddProviderRequest) { r.Kind = "--yes" },
		"key newlines": func(r *JevAddProviderRequest) { r.APIKey = "a\nb" },
	}
	for what, mutate := range bad {
		r := ok
		mutate(&r)
		if _, err := a.JevAddProvider(r); err == nil {
			t.Errorf("%s: must be refused", what)
		}
	}
	if _, err := a.JevUseProvider("", ""); err == nil {
		t.Error("clearing needs a surface")
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("the CLI ran for invalid input: %v", err)
	}
	// A kind the CLI does not list is refused after the list, before add.
	r := ok
	r.Kind = "typesafe"
	if _, err := a.JevAddProvider(r); err == nil || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("unlisted kind: err=%v", err)
	}
	if got := loggedArgs(t, argsLog); len(got) != 1 || !strings.HasSuffix(got[0], "jev provider list") {
		t.Fatalf("CLI calls = %v", got)
	}
}

// CLI errors (e.g. removing the default) come back as the error, unchanged.
func TestJevProviderCLIErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, "#!/bin/sh\necho 'local is the default provider; switch the default first' >&2\nexit 1\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	a := providerTestApp(t)
	if _, err := a.JevRemoveProvider("local"); err == nil || !strings.Contains(err.Error(), "switch the default first") {
		t.Fatalf("err = %v", err)
	}
}
