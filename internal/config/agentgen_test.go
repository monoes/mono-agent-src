package config

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/monomind"
)

// writeFakeIncompatibleMonomind mimics a pre-protocol monomind install: it
// answers unknown subcommands (including `--version --json`) with
// human-readable help text instead of JSON, exit 0 — the real
// installed-but-too-old case.
func writeFakeIncompatibleMonomind(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "monomind")
	script := "#!/bin/sh\necho 'Agent Management Commands'\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake monomind: %v", err)
	}
	return path
}

// TestGenerateConfigFailsFastOnIncompatibleMonomind guards against a
// regression where GenerateConfig resolved a runtime via Find()+Scan()
// without ever handshaking: against a too-old/incompatible monomind it
// would previously return a silent, useless success instead of the
// actionable "cache-only mode" error the Tier-3 fallback story depends on.
func TestGenerateConfigFailsFastOnIncompatibleMonomind(t *testing.T) {
	t.Setenv(monomind.EnvOverride, writeFakeIncompatibleMonomind(t))

	g := NewAgentGenerator(zerolog.Nop())
	_, err := g.GenerateConfig(context.Background(), "test-config", "<html></html>", "extract title", nil)
	if err == nil {
		t.Fatal("GenerateConfig() = nil error, want a cache-only/handshake error against an incompatible monomind")
	}
	if !strings.Contains(err.Error(), "cache-only mode") {
		t.Errorf("GenerateConfig() error = %q, want it to mention cache-only mode", err.Error())
	}
}

func TestGenerateConfigFailsFastWhenMonomindMissing(t *testing.T) {
	g := NewAgentGenerator(zerolog.Nop())
	// Simulate failed discovery without invoking a developer's system install.
	g.ensureMonomind = func(context.Context) (string, *monomind.VersionInfo, error) {
		return "", nil, &monomind.ErrNotFound{}
	}
	_, err := g.GenerateConfig(context.Background(), "test-config", "<html></html>", "extract title", nil)
	if err == nil {
		t.Fatal("GenerateConfig() = nil error, want a cache-only error when monomind is missing")
	}
	if !strings.Contains(err.Error(), "cache-only mode") {
		t.Errorf("GenerateConfig() error = %q, want it to mention cache-only mode", err.Error())
	}
}

// writeRecordingMonomind writes a fake monomind that answers the handshake
// and one `agent exec` turn, recording the exec argv (plus probes of the
// --cwd dir and --prompt-file contents) to the file named by
// AGENTGEN_RECORD. Runtime is pinned via RuntimeEnvVar so no scan is needed.
func writeRecordingMonomind(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "monomind")
	script := `#!/bin/sh
if [ "$1" = "--version" ] && [ "$2" = "--json" ]; then
  echo '{"v":1,"version":"2.10.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'
  exit 0
fi
if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then
  REC="${AGENTGEN_RECORD:?}"
  printf 'ARG %s\n' "$@" > "$REC"
  cwd=""; pf=""; prev=""
  for a in "$@"; do
    [ "$prev" = "--cwd" ] && cwd="$a"
    [ "$prev" = "--prompt-file" ] && pf="$a"
    prev="$a"
  done
  if [ -n "$cwd" ] && [ -d "$cwd" ] && [ -z "$(ls -A "$cwd")" ]; then
    echo "CWD_EMPTY_DIR yes" >> "$REC"
  else
    echo "CWD_EMPTY_DIR no ($cwd)" >> "$REC"
  fi
  if [ -n "$pf" ] && grep -q "GENERATE_MARKER_HTML" "$pf"; then
    echo "PROMPT_FILE_HAS_HTML yes" >> "$REC"
  else
    echo "PROMPT_FILE_HAS_HTML no" >> "$REC"
  fi
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"{\"config_name\":\"gen\",\"fields\":{\"title\":{\"xpath\":\"//title\",\"type\":\"text\",\"data\":null}}}"}'
  echo '{"v":1,"type":"done","exit_code":0}'
  exit 0
fi
echo 'unsupported invocation' >&2
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake monomind: %v", err)
	}
	return bin
}

// TestGenerateConfigSandboxesAgentExec is the RB3 sandboxing regression
// test: the exec turn must run with no tools (--tools none, never
// monomind's default), in a fresh empty temp cwd, under a spend cap, and
// with the HTML prompt delivered via --prompt-file rather than argv.
func TestGenerateConfigSandboxesAgentExec(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record.txt")
	t.Setenv("AGENTGEN_RECORD", record)
	t.Setenv(monomind.EnvOverride, writeRecordingMonomind(t))
	t.Setenv(RuntimeEnvVar, "claude")

	g := NewAgentGenerator(zerolog.Nop())
	m, err := g.GenerateConfig(context.Background(), "test-config", "<html>GENERATE_MARKER_HTML</html>", "extract title", nil)
	if err != nil {
		t.Fatalf("GenerateConfig: %v", err)
	}
	if m["config_name"] != "gen" {
		t.Errorf("config_name = %v, want %q", m["config_name"], "gen")
	}

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read exec record: %v", err)
	}
	rec := string(raw)

	// Prompt: via --prompt-file, never --prompt argv.
	if strings.Contains(rec, "ARG --prompt\n") {
		t.Errorf("exec passed --prompt argv; prompt must go via --prompt-file:\n%s", rec)
	}
	if !strings.Contains(rec, "ARG --prompt-file\n") || !strings.Contains(rec, "PROMPT_FILE_HAS_HTML yes") {
		t.Errorf("exec missing --prompt-file carrying the HTML prompt:\n%s", rec)
	}
	// Tools: explicitly none.
	if !strings.Contains(rec, "ARG --tools\n") || !strings.Contains(rec, "ARG none\n") {
		t.Errorf("exec missing explicit --tools none:\n%s", rec)
	}
	// Cwd: a fresh empty directory.
	if !strings.Contains(rec, "CWD_EMPTY_DIR yes") {
		t.Errorf("exec --cwd was not a fresh empty dir:\n%s", rec)
	}
	// Budget: hard spend cap present.
	if !strings.Contains(rec, "ARG --budget-usd\n") {
		t.Errorf("exec missing --budget-usd spend cap:\n%s", rec)
	}
}

func TestKiloConfigRuntimeDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name, agents, want string
	}{
		{"only Kilo", `[{"id":"kilo","installed":true}]`, "kilo"},
		{"existing preference", `[{"id":"kilo","installed":true},{"id":"codex","installed":true}]`, "codex"},
		{"not installed", `[{"id":"kilo","installed":false}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := writeRecordingMonomind(t)
			src, err := os.ReadFile(bin)
			if err != nil {
				t.Fatal(err)
			}
			scan := "if [ \"$1 $2\" = \"agent scan\" ]; then\n  echo '{\"v\":1,\"agents\":" + tc.agents + "}'\n  exit 0\nfi\n"
			src = []byte(strings.Replace(string(src), "echo 'unsupported invocation'", scan+"echo 'unsupported invocation'", 1))
			if err := os.WriteFile(bin, src, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv(monomind.EnvOverride, bin)
			t.Setenv(RuntimeEnvVar, "")
			monomind.ResetCapabilityCache()
			t.Cleanup(monomind.ResetCapabilityCache)
			g := NewAgentGenerator(zerolog.Nop())
			_, got, err := g.resolve(context.Background())
			if tc.want == "" {
				if err == nil || !strings.Contains(err.Error(), "cache-only mode") {
					t.Fatalf("uninstalled Kilo: %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("runtime = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
