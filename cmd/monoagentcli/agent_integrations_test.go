package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// stubAgentRuntimes puts only the named runtime CLIs "on PATH", records every
// CLI call instead of running it, and registers /opt/bin/monoagentcli.
func stubAgentRuntimes(t *testing.T, home string, onPath ...string) *[]string {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	var calls []string
	oldLook, oldRun, oldExe := agentLookPath, runRuntimeCLI, mcpExecutable
	t.Cleanup(func() { agentLookPath, runRuntimeCLI, mcpExecutable = oldLook, oldRun, oldExe })
	agentLookPath = func(name string) (string, error) {
		for _, n := range onPath {
			if n == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", exec.ErrNotFound
	}
	runRuntimeCLI = func(_ context.Context, _ func(string), name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	mcpExecutable = func() (string, error) { return "/opt/bin/monoagentcli", nil }
	return &calls
}

func writeAgentCfg(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAgentCfg(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func TestCodexMCPEntriesParsesConfigTOML(t *testing.T) {
	toml := `model = "o3"
# a comment
[mcp_servers.other]
command = "npx"
args = ["-y", "thing"]

[mcp_servers."ma"]
command = '/usr/local/bin/monoagentcli'
args = [
  "mcp",
  "--allow-mutations",
]

[mcp_servers.ma.env]
command = "not-this"

[mcp_servers.old]
command = "/x/monoagentcli" # trailing
args = ["mcp"] # trailing

[profiles.x]
command = "ignored"
`
	got := codexMCPEntries([]byte(toml))
	want := []mcpEntry{
		{name: "other", command: "npx", args: []string{"-y", "thing"}},
		{name: "ma", command: "/usr/local/bin/monoagentcli", args: []string{"mcp", "--allow-mutations"}},
		{name: "old", command: "/x/monoagentcli", args: []string{"mcp"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries:\n got %#v\nwant %#v", got, want)
	}
	var st mcpRuntimeState
	st.pickEntry(got)
	if !st.Registered || !st.Current || st.Entry != "ma" {
		t.Fatalf("pick: %+v", st)
	}
}

func TestMCPEntryIgnoresSpecialPurposeServers(t *testing.T) {
	for _, args := range [][]string{{"mcp", "--tasks-only", "--allow-mutations"}, {"mcp", "--api-only", "--allow-mutations"}, {"daemon"}} {
		if (mcpEntry{command: "monoagentcli", args: args}).isMonoagent() {
			t.Errorf("%v counted as the monoagent server", args)
		}
	}
}

// Only installed runtimes are reported; one outdated registration makes the
// whole thing unregistered, so doctor offers the fix.
func TestAgentsMCPRegistrationOnlyInstalledRuntimes(t *testing.T) {
	home := t.TempDir()
	stubAgentRuntimes(t, home, "codex")
	if found, _, _ := agentsMCPRegistration(); !found {
		t.Fatal("codex on PATH: want found")
	}
	writeAgentCfg(t, filepath.Join(home, ".codex", "config.toml"),
		"[mcp_servers.monoagent]\ncommand = \"/opt/bin/monoagentcli\"\nargs = [\"mcp\"]\n")
	found, reg, where := agentsMCPRegistration()
	if !found || reg || !strings.Contains(where, "Codex") || !strings.Contains(where, "outdated") {
		t.Fatalf("outdated codex: %v %v %q", found, reg, where)
	}
	if strings.Contains(where, "Claude") || strings.Contains(where, "Gemini") || strings.Contains(where, "opencode") {
		t.Fatalf("reports runtimes that are not installed: %q", where)
	}
	writeAgentCfg(t, filepath.Join(home, ".codex", "config.toml"),
		"[mcp_servers.monoagent]\ncommand = \"/opt/bin/monoagentcli\"\nargs = [\"mcp\", \"--allow-mutations\"]\n")
	if _, reg, where := agentsMCPRegistration(); !reg {
		t.Fatalf("current codex: %q", where)
	}

	none := t.TempDir()
	stubAgentRuntimes(t, none)
	if found, _, _ := agentsMCPRegistration(); found {
		t.Fatal("nothing installed: want not found")
	}
}

func TestRegisterAgentsMCP(t *testing.T) {
	home := t.TempDir()
	calls := stubAgentRuntimes(t, home, "claude", "codex", "opencode")
	writeAgentCfg(t, filepath.Join(home, ".claude.json"),
		`{"mcpServers":{"ma":{"command":"/usr/local/bin/monoagentcli","args":["mcp"]}}}`)
	writeAgentCfg(t, filepath.Join(home, ".codex", "config.toml"), "model = \"o3\"\n")
	ocPath := filepath.Join(home, ".config", "opencode", "opencode.json")
	writeAgentCfg(t, ocPath, `{"$schema":"https://opencode.ai/config.json","provider":{"x":{"a":1}},"mcp":{"keep":{"type":"remote","url":"https://e"}}}`)

	if err := registerAgentsMCP(context.Background(), func(string) {}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/bin/claude mcp remove --scope user ma",
		"/usr/bin/claude mcp add --scope user monoagent -- /opt/bin/monoagentcli mcp --allow-mutations",
		"/usr/bin/codex mcp add monoagent -- /opt/bin/monoagentcli mcp --allow-mutations",
	}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls:\n got %q\nwant %q", *calls, want)
	}
	if b, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml.bak")); err != nil || string(b) != "model = \"o3\"\n" {
		t.Fatalf("codex backup: %q %v", b, err)
	}

	oc := readAgentCfg(t, ocPath)
	if _, ok := oc["provider"]; !ok {
		t.Fatal("opencode: other settings lost")
	}
	mcp := oc["mcp"].(map[string]any)
	if _, ok := mcp["keep"]; !ok {
		t.Fatal("opencode: other server lost")
	}
	got := mcp["monoagent"].(map[string]any)
	if got["type"] != "local" || got["enabled"] != true ||
		!reflect.DeepEqual(got["command"], []any{"/opt/bin/monoagentcli", "mcp", "--allow-mutations"}) {
		t.Fatalf("opencode entry: %v", got)
	}
	if _, err := os.Stat(ocPath + ".bak"); err != nil {
		t.Fatalf("opencode backup: %v", err)
	}
	if st := opencodeMCPState(home); !st.Current {
		t.Fatalf("opencode state after register: %+v", st)
	}
	// Gemini is not installed: its config is never created.
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Fatalf("touched gemini config: %v", err)
	}
}

func TestRegisterGeminiReplacesOutdatedEntry(t *testing.T) {
	home := t.TempDir()
	stubAgentRuntimes(t, home, "gemini")
	path := geminiSettingsPath(home)
	writeAgentCfg(t, path, `{"theme":"dark","mcpServers":{"old":{"command":"/x/monoagentcli","args":["mcp"],"trust":true},"other":{"command":"npx"}}}`)

	if err := registerAgentsMCP(context.Background(), func(string) {}); err != nil {
		t.Fatal(err)
	}
	m := readAgentCfg(t, path)
	if m["theme"] != "dark" {
		t.Fatal("other settings lost")
	}
	servers := m["mcpServers"].(map[string]any)
	if _, ok := servers["old"]; ok {
		t.Fatal("outdated entry kept beside the new one")
	}
	if _, ok := servers["other"]; !ok {
		t.Fatal("other server lost")
	}
	e := servers["monoagent"].(map[string]any)
	if e["command"] != "/opt/bin/monoagentcli" || e["trust"] != true ||
		!reflect.DeepEqual(e["args"], []any{"mcp", "--allow-mutations"}) {
		t.Fatalf("entry: %v", e)
	}
	if b, _ := os.ReadFile(path + ".bak"); !strings.Contains(string(b), `"old"`) {
		t.Fatalf("backup: %q", b)
	}
}

func TestRegisterRefusesUnparsableConfig(t *testing.T) {
	home := t.TempDir()
	stubAgentRuntimes(t, home, "gemini")
	path := geminiSettingsPath(home)
	orig := "{\n  // a comment\n  \"theme\": \"dark\"\n}\n"
	writeAgentCfg(t, path, orig)
	err := registerAgentsMCP(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "not editing") {
		t.Fatalf("want refusal, got %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != orig {
		t.Fatalf("config changed: %q", b)
	}
	if found, reg, where := agentsMCPRegistration(); !found || reg || !strings.Contains(where, "cannot parse") {
		t.Fatalf("state: %v %v %q", found, reg, where)
	}
}

func TestRegisterAgentsMCPReportsErrorsAndContinues(t *testing.T) {
	home := t.TempDir()
	calls := stubAgentRuntimes(t, home, "claude", "codex")
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	runRuntimeCLI = func(_ context.Context, _ func(string), name string, args ...string) error {
		*calls = append(*calls, name)
		if strings.HasSuffix(name, "claude") {
			return errors.New("boom")
		}
		return nil
	}
	err := registerAgentsMCP(context.Background(), func(string) {})
	if err == nil || !strings.Contains(err.Error(), "Claude Code: boom") {
		t.Fatalf("err %v", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("codex not tried after claude failed: %v", *calls)
	}
}

func TestSkillsInstallForOtherRuntimes(t *testing.T) {
	home := t.TempDir()
	stubAgentRuntimes(t, home)
	for _, d := range []string{".codex", ".gemini", filepath.Join(".config", "opencode")} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// No ~/.claude: opencode gets its own copy.
	found, missing, _ := claudeSkillsState()
	if !found || len(missing) != 3*len(claudeSkillNames) {
		t.Fatalf("found %v missing %v", found, missing)
	}
	runClaudeFirstRunCheck()
	for _, dir := range []string{".codex/skills", ".gemini/skills", ".config/opencode/skills"} {
		for _, name := range claudeSkillNames {
			if _, err := os.Stat(filepath.Join(home, dir, name, "SKILL.md")); err != nil {
				t.Errorf("%s/%s: %v", dir, name, err)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".claude")); !os.IsNotExist(err) {
		t.Fatal("first-run check created ~/.claude")
	}
	if _, missing, stale := claudeSkillsState(); len(missing)+len(stale) != 0 {
		t.Fatalf("after install: missing %v stale %v", missing, stale)
	}
	writeAgentCfg(t, filepath.Join(home, ".codex", "skills", "monoagent-build", "SKILL.md"), "mine")
	runClaudeFirstRunCheck()
	if b, _ := os.ReadFile(filepath.Join(home, ".codex", "skills", "monoagent-build", "SKILL.md")); string(b) != "mine" {
		t.Fatal("first-run check rewrote an existing skill")
	}
	if _, _, stale := claudeSkillsState(); len(stale) != 1 || stale[0] != "monoagent-build (Codex)" {
		t.Fatalf("stale %v", stale)
	}

	// With ~/.claude, opencode reads ~/.claude/skills: no second copy.
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tg := range skillTargets(home, false) {
		if tg.label == "opencode" {
			t.Fatal("opencode target listed beside Claude Code")
		}
	}
	if err := installClaudeSkill(false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".codex", "skills", "monoagent-build", "SKILL.md")); string(b) == "mine" {
		t.Fatal("init --claude should refresh every runtime's copy")
	}
}

func TestSkillsNoRuntimeNoWrites(t *testing.T) {
	home := t.TempDir()
	stubAgentRuntimes(t, home)
	if found, _, _ := claudeSkillsState(); found {
		t.Fatal("no runtime folder: want not found")
	}
	runClaudeFirstRunCheck()
	for _, d := range []string{".claude", ".codex", ".gemini", ".config"} {
		if _, err := os.Stat(filepath.Join(home, d)); !os.IsNotExist(err) {
			t.Errorf("created %s", d)
		}
	}
}
