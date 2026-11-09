package openaiapi

import (
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/testdb"
)

// fakeMonomind is a shell script that speaks just enough of the Agent Exec
// Protocol for the gateway: the handshake, `agent scan`, `agent models` and
// `agent exec` (it records the argv of the exec call next to itself, then
// answers with a scripted turn).
const fakeMonomind = `#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
if [ "$1" = "--version" ] && [ "$2" = "--json" ]; then
  echo '{"v":1,"version":"2.22.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1","agent-models"]}'
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "scan" ]; then
  echo '{"v":1,"agents":[{"id":"claude","installed":true,"binary":"/usr/local/bin/claude","version":"2.1.0","install_hint":"","streams_incrementally":true,"native_sandbox":"monomind","sandbox_modes":["read-only","workspace-write","full"]}]}'
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "models" ]; then
  echo '{"v":1,"runtime":"claude","supported":true,"models":[{"id":"default","label":"Default"},{"id":"sonnet","label":"Sonnet"}]}'
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then
  printf '%s\n' "$@" > "$DIR/exec-argv.txt"
  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/x","pid":1,"streams_incrementally":true,"native_sandbox":"monomind","access":"scoped"}'
  echo '{"v":1,"type":"session","session_id":"th_fake"}'
  echo '{"v":1,"type":"assistant","text":"Hello from "}'
  echo '{"v":1,"type":"assistant","text":"the fake"}'
  echo '{"v":1,"type":"usage","input_tokens":12,"output_tokens":5}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","input_tokens":12,"output_tokens":5}'
  echo '{"v":1,"type":"done","exit_code":0}'
  exit 0
fi
echo "unsupported: $*" >&2
exit 2
`

// TestEndToEndThroughTheRealExec runs the gateway's default wiring (real
// monomind.Exec, Scan and ListModels) against the fake binary: what reaches
// the runner's command line is exactly the locked-down text posture.
func TestEndToEndThroughTheRealExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake monomind is a shell script")
	}
	home := t.TempDir()
	testhome.SetHome(t, home)
	binDir := t.TempDir() // outside the turn's folder: monomind.Exec refuses a binary inside its cwd
	bin := filepath.Join(binDir, "monomind")
	if err := os.WriteFile(bin, []byte(fakeMonomind), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)

	db := testdb.Open(t)

	deps := DefaultDeps(db.DB, "e2e")
	deps.Logf = func(string, ...any) {}
	g, err := New(deps, Config{ScratchRoot: filepath.Join(home, "scratch")})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := apikeys.NewStore(db.DB).Create(context.Background(), "default", "e2e", false)
	if err != nil {
		t.Fatal(err)
	}

	h := &harness{g: g}
	models := h.serve(Policy{Max: ChatOnly}, http.MethodGet, "/v1/models", secret, "")
	list := decodeModelList(t, models)
	if models.Code != 200 || len(list.Data) != 2 || list.Data[0].ID != "claude/default" || list.Data[1].ID != "claude/sonnet" {
		t.Fatalf("models: %d %s", models.Code, models.Body)
	}

	rec := h.serve(Policy{Max: ChatOnly}, http.MethodPost, "/v1/chat/completions", secret,
		`{"model":"claude/sonnet","messages":[{"role":"system","content":"Be brief."},{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 {
		t.Fatalf("chat: %d %s", rec.Code, rec.Body)
	}
	var got completion
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Choices[0].Message.Content != "Hello from the fake" || got.Model != "claude/sonnet" ||
		got.Usage == nil || got.Usage.PromptTokens != 12 || got.Usage.CompletionTokens != 5 {
		t.Fatalf("completion: %+v", got)
	}

	argvRaw, err := os.ReadFile(filepath.Join(binDir, "exec-argv.txt"))
	if err != nil {
		t.Fatalf("the fake never saw an exec call: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(argvRaw)), "\n")
	has := func(flag, value string) bool {
		for i, a := range argv {
			if a == flag && (value == "" || (i+1 < len(argv) && argv[i+1] == value)) {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ flag, value string }{
		{"--runtime", "claude"}, {"--model", "sonnet"}, {"--tools", "none"}, {"--system-file", ""}, {"--prompt-file", ""}, {"--cwd", ""},
	} {
		if !has(c.flag, c.value) {
			t.Errorf("argv lacks %s %s:\n%s", c.flag, c.value, strings.Join(argv, " "))
		}
	}
	for _, forbidden := range []string{"--access", "--settings", "--allow-bash-prefix", "--tools-file", "--env"} {
		if has(forbidden, "") {
			t.Errorf("a text turn must not pass %s:\n%s", forbidden, strings.Join(argv, " "))
		}
	}
	cwd := ""
	for i, a := range argv {
		if a == "--cwd" && i+1 < len(argv) {
			cwd = argv[i+1]
		}
		// The prompt files are in a private folder, not in the temp directory.
		if (a == "--prompt-file" || a == "--system-file") && i+1 < len(argv) && !strings.HasPrefix(argv[i+1], filepath.Join(home, "scratch", ".tmp")+string(filepath.Separator)) {
			t.Errorf("%s = %q, want a file in the private folder under %s", a, argv[i+1], filepath.Join(home, "scratch", ".tmp"))
		}
	}
	if cwd != filepath.Join(home, "scratch", profileFolder("default"), "slot-0") {
		t.Errorf("--cwd = %q, want the profile's slot folder under the scratch root", cwd)
	}
	if entries, err := os.ReadDir(cwd); err != nil || len(entries) != 0 {
		t.Errorf("the turn's folder must be left empty afterwards (%d entries, err = %v)", len(entries), err)
	}
}

// TestDefaultDepsReadTheRosterFromTheDatabase checks the default Deps read
// the validated roster from the same database the keys live in.
func TestDefaultDepsReadTheRosterFromTheDatabase(t *testing.T) {
	db := testdb.Open(t)
	if err := agentroster.AddManual(context.Background(), db.DB, "claude", "sonnet"); err != nil {
		t.Fatal(err)
	}

	results, err := DefaultDeps(db.DB, "x").Catalog.Roster(context.Background())
	if err != nil || len(results) != 1 || results[0].Model != "sonnet" {
		t.Fatalf("roster = %+v, %v", results, err)
	}
}
