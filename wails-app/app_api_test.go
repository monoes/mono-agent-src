package main

import (
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAPIKey is a stand-in for the key `api key create` prints once, built
// at run time so that no key-shaped literal sits in the source. It must never
// appear in an argv line, nor in any method's result but the create's.
var fakeAPIKey = "sk-ma-" + strings.Repeat("F", 43)

const (
	apiStatusJSON = `{"v":1,"profile":"work","keys":{"active":2},` +
		`"daemon":{"running":true,"api_addr":"127.0.0.1:9322","v1_addr":"0.0.0.0:9443"},` +
		`"auto":{"available":false,"missing":"the api_auto surface switched on for the profile (monoagentcli jev enable api_auto)"},` +
		`"listeners":[` +
		`{"name":"main","addr":"127.0.0.1:9322","loopback":true,"v1":true,"confinement":"any","context_confinement":"chat-only","auto_confinement":"chat-only","confinement_source":"daemon","reachable":true,"v1_answers":true},` +
		`{"name":"v1","addr":"0.0.0.0:9443","loopback":false,"v1":true,"confinement":"chat-only","context_confinement":"chat-only","auto_confinement":"chat-only","confinement_source":"daemon","reachable":true,"v1_answers":true,"a_field_from_the_future":{"x":1}}]}`

	apiModelsJSON = `{"v":1,"policy":{"for":"network","confinement":"chat-only","context_confinement":"chat-only","auto_confinement":"chat-only","source":"shell"},` +
		`"models":[` +
		`{"id":"claude/default","runtime":"claude","model":"default","label":"Default","confinement":"chat-only","validated":true,"allowed":true,"context_allowed":true,"auto_allowed":true,"capabilities":["text","tools"]},` +
		`{"id":"codex/gpt-6-astra","runtime":"codex","model":"gpt-6-astra","label":"GPT-6-Astra","confinement":"sandboxed","validated":false,"allowed":false,"context_allowed":false,"auto_allowed":false,"capabilities":["text","image","tools"]}],` +
		`"auto":{"available":true,"key_source":"vault","confinement":"chat-only","candidates":1,"held_back":2}}`

	apiKeyOne = `{"id":"key_abcdefghijkl","profile_id":"work","name":"my-app","prefix":"sk-ma-AbCdEf","context":false,` +
		`"created_at":"2026-10-01T09:15:00Z","last_used_at":"2026-10-02T08:30:00Z","revoked_at":null}`
	apiKeyTwo = `{"id":"key_mnopqrstuvwx","profile_id":"work","name":"notes bot","prefix":"sk-ma-GhIjKl","context":true,` +
		`"created_at":"2026-10-02T07:00:00Z","last_used_at":null,"revoked_at":null}`
)

// argv is one call as fakeAPICLI logs it: every argument in brackets, so that
// `--name=notes bot` (one argument) is not the same line as `--name=notes bot`
// as two arguments, which a log of "$*" cannot tell.
func argv(args ...string) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString("[" + a + "]")
	}
	return b.String()
}

// fakeAPICLI installs a monoagentcli stand-in that logs argv (one line per
// call, in the form argv builds) and answers each api subcommand with a canned
// JSON document. The create and the update answers carry a key, so that a method
// that let one through would show.
func fakeAPICLI(t *testing.T) (argsLog string) {
	t.Helper()
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	withKey := strings.TrimSuffix(apiKeyOne, "}") + `,"key":"` + fakeAPIKey + `"}`
	revoked := strings.Replace(apiKeyOne, `"revoked_at":null`, `"revoked_at":"2026-10-02T10:00:00Z"`, 1)
	script := `#!/bin/sh
line=''
for a in "$@"; do line="$line[$a]"; done
echo "$line" >> '` + argsLog + `'
case "$*" in
  *" api status"*) echo '` + apiStatusJSON + `';;
  *" api models"*) echo '` + apiModelsJSON + `';;
  *" api key list"*) echo '[` + apiKeyOne + `,` + apiKeyTwo + `]';;
  *" api key create"*) echo '` + withKey + `';;
  *" api key update"*) echo '` + withKey + `';;
  *" api key revoke"*) echo '` + revoked + `';;
  *) echo 'unexpected' >&2; exit 2;;
esac
`
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	return argsLog
}

func TestAPIFuncsShellOutToCLI(t *testing.T) {
	argsLog := fakeAPICLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	st, err := a.APIStatus()
	if err != nil || st.Profile != "work" || st.Keys.Active != 2 || !st.Daemon.Running || st.Daemon.V1Addr != "0.0.0.0:9443" ||
		st.Auto == nil || st.Auto.Available || !strings.Contains(st.Auto.Missing, "api_auto") || len(st.Listeners) != 2 {
		t.Fatalf("APIStatus = %+v, %v", st, err)
	}
	if l := st.Listeners[1]; l.Name != "v1" || l.Loopback || !l.V1 || l.Confinement != "chat-only" || l.AutoConfinement != "chat-only" ||
		l.ConfinementSource != "daemon" || !l.Reachable || !l.V1Answers {
		t.Fatalf("v1 listener = %+v", l)
	}

	// No policy given: the CLI's own defaults.
	if _, err := a.APIModels("", "", "", ""); err != nil {
		t.Fatal(err)
	}
	m, err := a.APIModels("network", "chat-only", "chat-only", "chat-only")
	if err != nil || m.Policy.For != "network" || m.Policy.AutoConfinement != "chat-only" || len(m.Models) != 2 ||
		m.Auto == nil || !m.Auto.Available || m.Auto.Candidates == nil || *m.Auto.Candidates != 1 || m.Auto.HeldBack == nil || *m.Auto.HeldBack != 2 ||
		m.Auto.Confinement != "chat-only" || m.Auto.KeySource != "vault" {
		t.Fatalf("APIModels = %+v, %v", m, err)
	}
	if x := m.Models[1]; x.ID != "codex/gpt-6-astra" || x.Confinement != "sandboxed" || x.Allowed || x.ContextAllowed || x.AutoAllowed == nil || *x.AutoAllowed || x.Validated {
		t.Fatalf("model = %+v", x)
	}
	// What each model can do reaches the page as the CLI listed it.
	if got := strings.Join(m.Models[0].Capabilities, ","); got != "text,tools" {
		t.Fatalf("claude/default capabilities = %q", got)
	}
	if got := strings.Join(m.Models[1].Capabilities, ","); got != "text,image,tools" {
		t.Fatalf("codex/gpt-6-astra capabilities = %q", got)
	}

	keys, err := a.APIKeyList()
	if err != nil || len(keys) != 2 || keys[0].Name != "my-app" || keys[0].Prefix != "sk-ma-AbCdEf" || keys[0].LastUsedAt == "" ||
		keys[1].LastUsedAt != "" || !keys[1].Context || keys[0].RevokedAt != "" {
		t.Fatalf("APIKeyList = %+v, %v", keys, err)
	}

	created, err := a.APIKeyCreate("my-app", false)
	if err != nil || created.Key != fakeAPIKey || created.ID != "key_abcdefghijkl" || created.Name != "my-app" {
		t.Fatalf("APIKeyCreate = %+v, %v", created.APIKey, err)
	}
	if _, err := a.APIKeyCreate("notes bot", true); err != nil {
		t.Fatal(err)
	}
	// Only the create returns a key: what update answers is metadata.
	updated, err := a.APIKeySetContext("key_abcdefghijkl", true)
	if err != nil || updated.ID != "key_abcdefghijkl" {
		t.Fatalf("APIKeySetContext = %+v, %v", updated, err)
	}
	if raw, _ := json.Marshal(updated); strings.Contains(string(raw), fakeAPIKey) {
		t.Fatal("a key came out of APIKeySetContext")
	}
	if _, err := a.APIKeySetContext("key_abcdefghijkl", false); err != nil {
		t.Fatal(err)
	}
	revoked, err := a.APIKeyRevoke("key_abcdefghijkl")
	if err != nil || revoked.RevokedAt != "2026-10-02T10:00:00Z" {
		t.Fatalf("APIKeyRevoke = %+v, %v", revoked, err)
	}

	pre := []string{"--profile", "work", "--json", "api"}
	call := func(args ...string) string { return argv(append(append([]string{}, pre...), args...)...) }
	want := []string{
		call("status"),
		call("models"),
		call("models", "--for=network", "--confinement=chat-only", "--context-confinement=chat-only", "--auto-confinement=chat-only"),
		call("key", "list"),
		call("key", "create", "--name=my-app"),
		call("key", "create", "--name=notes bot", "--context"), // the name is one argument, with its space
		call("key", "update", "key_abcdefghijkl", "--context"),
		call("key", "update", "key_abcdefghijkl", "--no-context"),
		call("key", "revoke", "key_abcdefghijkl", "--yes"),
	}
	got := loggedArgs(t, argsLog)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if raw, _ := os.ReadFile(argsLog); strings.Contains(string(raw), fakeAPIKey) {
		t.Fatal("a key appeared in argv")
	}
}

// Only the flags that were given reach the CLI, and a value stays the value of
// its flag whatever it looks like.
func TestAPIModelsPassesOnlyWhatWasGiven(t *testing.T) {
	argsLog := fakeAPICLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	if _, err := a.APIModels("loopback", "", "sandboxed", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.APIKeyCreate("--yes", false); err != nil { // the CLI refuses the name; the binding must not let it become a flag
		t.Fatal(err)
	}
	if _, err := a.APIKeyCreate("  padded  ", false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		argv("--profile", "work", "--json", "api", "models", "--for=loopback", "--context-confinement=sandboxed"),
		argv("--profile", "work", "--json", "api", "key", "create", "--name=--yes"),
		argv("--profile", "work", "--json", "api", "key", "create", "--name=padded"),
	}
	if got := loggedArgs(t, argsLog); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Renaming a key is `api key update <id> --name=<name>` and nothing else: it never
// touches the context switch, a name stays one argument attached to its flag
// whatever it looks like, and what the CLI answers is the key's metadata (the fake
// answers with a key, as it does for the create, so that a method that let one
// through would show).
func TestAPIKeyRenameShellsOutToCLI(t *testing.T) {
	argsLog := fakeAPICLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	renamed, err := a.APIKeyRename("key_abcdefghijkl", "  my renamed app  ")
	if err != nil || renamed.ID != "key_abcdefghijkl" || renamed.Prefix != "sk-ma-AbCdEf" {
		t.Fatalf("APIKeyRename = %+v, %v", renamed, err)
	}
	if raw, _ := json.Marshal(renamed); strings.Contains(string(raw), fakeAPIKey) {
		t.Fatal("a key came out of APIKeyRename")
	}
	if _, err := a.APIKeyRename("key_abcdefghijkl", "--context"); err != nil { // a name, not a flag
		t.Fatal(err)
	}
	want := []string{
		argv("--profile", "work", "--json", "api", "key", "update", "key_abcdefghijkl", "--name=my renamed app"),
		argv("--profile", "work", "--json", "api", "key", "update", "key_abcdefghijkl", "--name=--context"),
	}
	got := loggedArgs(t, argsLog)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if raw, _ := os.ReadFile(argsLog); strings.Contains(string(raw), fakeAPIKey) || strings.Contains(string(raw), "[--context]") || strings.Contains(string(raw), "[--no-context]") {
		t.Fatal("a rename passed a key or touched the context switch")
	}
}

func TestAPIRefusesBadInputBeforeCLI(t *testing.T) {
	argsLog := fakeAPICLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	if _, err := a.APIKeyCreate("   ", false); err == nil {
		t.Fatal("an empty name must be refused")
	}
	if _, err := a.APIKeyRename("key_abcdefghijkl", "   "); err == nil {
		t.Fatal("an empty new name must be refused")
	}
	for _, id := range []string{"", "  ", "--yes", "-x", " --profile"} {
		if _, err := a.APIKeySetContext(id, true); err == nil {
			t.Errorf("APIKeySetContext(%q) must be refused", id)
		}
		if _, err := a.APIKeyRevoke(id); err == nil {
			t.Errorf("APIKeyRevoke(%q) must be refused", id)
		}
		if _, err := a.APIKeyRename(id, "new name"); err == nil {
			t.Errorf("APIKeyRename(%q) must be refused", id)
		}
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("the CLI ran for invalid input: %v", err)
	}
}

// An empty list is [] and not null, so the page can map over it.
func TestAPIEmptyListsAreNotNull(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, fakeCLIName)
	script := `#!/bin/sh
case "$*" in
  *" api status"*) echo '{"profile":"work","keys":{"active":0},"daemon":{"running":false},"auto":{"available":false},"listeners":null}';;
  *" api models"*) echo '{"policy":{},"models":null,"auto":{"available":false}}';;
  *" api key list"*) echo 'null';;
esac
`
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	a := newTestApp(t)
	a.ctx = context.Background()
	st, err := a.APIStatus()
	if err != nil || st.Listeners == nil {
		t.Fatalf("APIStatus listeners = %#v, %v", st.Listeners, err)
	}
	m, err := a.APIModels("", "", "", "")
	if err != nil || m.Models == nil {
		t.Fatalf("APIModels models = %#v, %v", m.Models, err)
	}
	keys, err := a.APIKeyList()
	if err != nil || keys == nil || len(keys) != 0 {
		t.Fatalf("APIKeyList = %#v, %v", keys, err)
	}
}

// CLI errors (stderr) surface as the returned error: the duplicate-name and
// not-found messages the CLI words are the ones the page shows.
func TestAPICLIErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, "#!/bin/sh\necho 'an active key with that name already exists in this profile' >&2\nexit 3\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	a := newTestApp(t)
	a.ctx = context.Background()
	if _, err := a.APIKeyCreate("my-app", false); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	if _, err := a.APIStatus(); err == nil {
		t.Fatal("a failing CLI must be an error")
	}
}

// A failed call keeps the CLI's own classification (exit 2 not found, 3 invalid
// input), which the page needs to word it, and the message is the line the CLI
// printed last, after any warnings.
func TestAPICLIErrorsKeepTheirClass(t *testing.T) {
	for _, c := range []struct{ name, script, want string }{
		{"invalid input", "echo 'an active key with that name already exists in this profile' >&2\nexit 3", "invalid_input: an active key with that name already exists in this profile"},
		{"not found", "echo 'api key not found' >&2\nexit 2", "not_found: api key not found"},
		{"anything else is the message alone", "echo 'boom' >&2\nexit 1", "boom"},
		{"the line it printed last", "echo '2026/10/02 12:00:00 applied migration 061' >&2\necho 'api key not found' >&2\nexit 2", "not_found: api key not found"},
		{"no message", "exit 4", "exit status 4"},
	} {
		t.Run(c.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), fakeCLIName)
			if err := writeFakeCLI(bin, "#!/bin/sh\n"+c.script+"\n"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MONOAGENTCLI_BIN", bin)
			a := newTestApp(t)
			a.ctx = context.Background()
			for name, call := range map[string]func() error{
				"APIKeyRevoke": func() error { _, err := a.APIKeyRevoke("key_abcdefghijkl"); return err },
				"APIKeyRename": func() error { _, err := a.APIKeyRename("key_abcdefghijkl", "renamed"); return err },
				"APIStatus":    func() error { _, err := a.APIStatus(); return err },
			} {
				if err := call(); err == nil || err.Error() != c.want {
					t.Errorf("%s: error = %v, want %q", name, err, c.want)
				}
			}
		})
	}
}

// TestAPIRealCLI drives the real monoagentcli (opt-in: API_REAL_CLI=1 with
// MONOAGENTCLI_BIN set): the shapes of its JSON are the ones the structs here
// decode. It never prints a key. What it touches:
//
//   - HOME and USERPROFILE (the home of Unix and of Windows) are a scratch
//     directory. The CLI keeps its state under ~/.monoagent, the database and the
//     key store with it, so the keys this test creates and revokes exist only
//     there, and the test stops before it creates one if the CLI did not put its
//     state there.
//   - MONOAGENT_DAEMON_HEARTBEAT names a file that does not exist, so no running
//     daemon is found, and MONOAGENT_HTTPAPI_ADDR points where nothing listens
//     (MONOAGENT_API_V1_ADDR is empty), so `api status` finds the API down.
//   - Not isolated: PATH and the rest of the environment. `api models` runs the
//     real monomind, found the way the CLI finds it, to list the agent runtimes
//     installed on this machine; it sends them no prompt.
func TestAPIRealCLI(t *testing.T) {
	if os.Getenv("API_REAL_CLI") != "1" || os.Getenv("MONOAGENTCLI_BIN") == "" {
		t.Skip("set API_REAL_CLI=1 and MONOAGENTCLI_BIN to run against the real CLI")
	}
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(t.TempDir(), "none.json"))
	t.Setenv("MONOAGENT_HTTPAPI_ADDR", "127.0.0.1:1")
	t.Setenv("MONOAGENT_API_V1_ADDR", "")
	a := newTestApp(t)
	a.ctx = context.Background()

	if keys, err := a.APIKeyList(); err != nil || len(keys) != 0 {
		t.Fatalf("initial keys = %+v, %v", keys, err)
	}
	// The first call made the CLI open its database: it must be the scratch one, before any key exists.
	if _, err := os.Stat(filepath.Join(home, ".monoagent")); err != nil {
		t.Fatalf("the CLI did not keep its state under the scratch home: %v", err)
	}
	created, err := a.APIKeyCreate("real-cli", false)
	if err != nil || created.ID == "" || created.Name != "real-cli" || created.Context || created.CreatedAt == "" ||
		created.LastUsedAt != "" || created.RevokedAt != "" || created.ProfileID == "" {
		t.Fatalf("APIKeyCreate metadata = %+v, %v", created.APIKey, err)
	}
	if !strings.HasPrefix(created.Key, "sk-ma-") || len(created.Key) != 49 || !strings.HasPrefix(created.Key, created.Prefix) {
		t.Fatalf("the created key is not a key (length %d)", len(created.Key))
	}
	if _, err := a.APIKeyCreate("real-cli", false); err == nil {
		t.Fatal("a second key with the same name must be refused")
	}
	keys, err := a.APIKeyList()
	if err != nil || len(keys) != 1 || keys[0].ID != created.ID || keys[0].Prefix != created.Prefix || keys[0].Context {
		t.Fatalf("keys after create = %+v, %v", keys, err)
	}
	if k, err := a.APIKeySetContext(created.ID, true); err != nil || !k.Context {
		t.Fatalf("context on = %+v, %v", k, err)
	}
	if k, err := a.APIKeySetContext(created.ID, false); err != nil || k.Context {
		t.Fatalf("context off = %+v, %v", k, err)
	}
	if _, err := a.APIKeySetContext("key_doesnotexist", true); err == nil {
		t.Fatal("an unknown key must be an error")
	}

	st, err := a.APIStatus()
	if err != nil || st.Profile == "" || st.Keys.Active != 1 || st.Listeners == nil {
		t.Fatalf("status = %+v, %v", st, err)
	}
	for _, l := range st.Listeners {
		if l.Name == "" || l.Addr == "" || l.Confinement == "" || l.ContextConfinement == "" || l.AutoConfinement == "" || l.ConfinementSource == "" {
			t.Fatalf("a listener lacks a field: %+v", l)
		}
	}
	m, err := a.APIModels("network", "", "", "")
	if err != nil || m.Policy.For != "network" || m.Policy.Confinement != "chat-only" || m.Policy.AutoConfinement == "" || m.Models == nil {
		t.Fatalf("models = %+v, %v", m, err)
	}
	for _, x := range m.Models {
		if x.ID == "" || x.Runtime == "" || x.Confinement == "" {
			t.Fatalf("a model lacks a field: %+v", x)
		}
	}

	revoked, err := a.APIKeyRevoke(created.ID)
	if err != nil || revoked.RevokedAt == "" {
		t.Fatalf("revoke = %+v, %v", revoked, err)
	}
	if keys, err := a.APIKeyList(); err != nil || len(keys) != 0 {
		t.Fatalf("keys after revoke = %+v, %v", keys, err)
	}
}
