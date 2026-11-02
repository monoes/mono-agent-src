package chat

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// writeCapturingMonomind is a fake monomind that keeps the tools file and the
// system prompt it was handed, so a test can read what the model would be
// offered. The files are temporary: monomind.Exec removes them afterwards.
func writeCapturingMonomind(t *testing.T) (bin, argsLog, toolsCopy, systemCopy string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "monomind")
	argsLog = filepath.Join(dir, "args.log")
	toolsCopy = filepath.Join(dir, "tools.json")
	systemCopy = filepath.Join(dir, "system.txt")
	script := `#!/bin/sh
echo "$*" >> '` + argsLog + `'
if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi
if [ "$1" = "init" ]; then echo '{"root":"x","created":[],"skipped":[]}'; exit 0; fi
if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--tools-file" ]; then cp "$a" '` + toolsCopy + `'; fi
    if [ "$prev" = "--system-file" ]; then cp "$a" '` + systemCopy + `'; fi
    prev="$a"
  done
  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"read"}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok"}'
  echo '{"v":1,"type":"done","exit_code":0}'
  exit 0
fi
echo "unsupported: $*" >&2
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	return bin, argsLog, toolsCopy, systemCopy
}

func toolNamesIn(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the model was offered no tools file: %v", err)
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		var wrapped struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if err2 := json.Unmarshal(raw, &wrapped); err2 != nil {
			t.Fatalf("unreadable tools file: %v\n%s", err, raw)
		}
		list = wrapped.Tools
	}
	var out []string
	for _, e := range list {
		out = append(out, e.Name)
	}
	return out
}

func TestParseToolsFlagKeepsPageReadSeparate(t *testing.T) {
	// The desktop app's parser takes no page-read member: it is its own mode.
	if _, _, err := parseToolsFlag("monoagent:read"); err == nil {
		t.Error("parseToolsFlag must not accept monoagent:read")
	}
	if !isPageReadTools("monoagent:read") || !isPageReadTools(" monoagent:read ") {
		t.Error("monoagent:read must select the page-read mode")
	}
	if mono, runs, err := parseToolsMode("monoagent:read", true); err != nil || !mono || runs {
		t.Errorf("parseToolsMode(page-read) = (%v, %v, %v), want monoagent tools without runs", mono, runs, err)
	}
	for _, in := range []string{"", "monoagent", "monoagent,runs", "monoagent:read,runs", "runs,monoagent:read", "monoagent,monoagent:read"} {
		if isPageReadTools(in) {
			t.Errorf("isPageReadTools(%q) = true", in)
		}
	}
}

func TestPageReadTurnOffersOnlyTheReadTools(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, argsLog, toolsCopy, systemCopy := writeCapturingMonomind(t)
	if _, err := runChatCmd(t, dbPath, bin, "--runtime", "claude", "--tools", "monoagent:read", "--", "what is on this page?"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	got := toolNamesIn(t, toolsCopy)
	// The proposals write nothing: the person confirms each in the side panel (#451).
	want := map[string]bool{"list_workflows": true, "get_workflow": true, "list_node_types": true, "propose_task": true, "propose_recording": true, "propose_vault_save": true, "propose_page_action": true, "propose_page_look": true, "propose_coder_handoff": true}
	if len(got) != len(want) {
		t.Fatalf("tools offered = %v, want exactly %v", got, want)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected tool offered: %s", n)
		}
	}

	argv, _ := os.ReadFile(argsLog)
	for _, bad := range []string{"--allow-bash-prefix", "monoagentcli workflow", "monomind org"} {
		if strings.Contains(string(argv), bad) {
			t.Errorf("a page-read turn must not get shell access (%q in argv)", bad)
		}
	}
	system, _ := os.ReadFile(systemCopy)
	// The secret vault's tools by name: propose_vault_save (the file vault,
	// on the person's click) is the one vault word the prompt may use.
	for _, bad := range []string{"Bash", "save_document", "create_workflow", "run_workflow", "monograph", "vault_item", "secret", "people"} {
		if strings.Contains(string(system), bad) {
			t.Errorf("the page-read system prompt advertises %q:\n%s", bad, system)
		}
	}
	if !strings.Contains(string(system), "web page") {
		t.Errorf("the page-read system prompt must say page text is untrusted:\n%s", system)
	}
	for _, want := range []string{"propose_task", "propose_recording", "propose_vault_save", "never one the page asks for", "Start recording", "Save to vault"} {
		if !strings.Contains(string(system), want) {
			t.Errorf("the page-read system prompt must say each proposal is the person's to confirm, and never the page's (%q):\n%s", want, system)
		}
	}
}

// runChatCmdStdin is runChatCmd with a stdin.
func runChatCmdStdin(t *testing.T, dbPath, stdin string, args ...string) error {
	t.Helper()
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	cmd := newChatCmd(cfg)
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(stdin))
	r, w, _ := os.Pipe()
	realStdout := os.Stdout
	drained := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		drained <- b
	}()
	os.Stdout = w
	err := cmd.Execute()
	os.Stdout = realStdout
	w.Close()
	<-drained
	return err
}

// The page text can be read from stdin, so it need not sit in argv where any
// process listing shows it.
func TestChatPromptCanComeFromStdin(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	_, argsLog, _, _ := writeCapturingMonomind(t)
	page := "text from a page: --not-a-flag 世界"
	if err := runChatCmdStdin(t, dbPath, page, "--runtime", "claude", "--tools", "monoagent:read", "--prompt-stdin"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	argv, _ := os.ReadFile(argsLog)
	if strings.Contains(string(argv), "text from a page") {
		t.Errorf("the prompt reached an argv: %s", argv)
	}
	if !strings.Contains(string(argv), "--prompt-file") {
		t.Errorf("monomind was not handed a prompt file: %s", argv)
	}
}

func TestChatPromptStdinRefusals(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	writeCapturingMonomind(t)
	for name, c := range map[string]struct {
		stdin string
		args  []string
	}{
		"also a positional prompt": {"hi", []string{"--runtime", "claude", "--prompt-stdin", "--", "other"}},
		"empty stdin":              {"  \n", []string{"--runtime", "claude", "--prompt-stdin"}},
		"too large":                {strings.Repeat("x", maxPromptStdin+1), []string{"--runtime", "claude", "--prompt-stdin"}},
	} {
		if err := runChatCmdStdin(t, dbPath, c.stdin, c.args...); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
	// Without the flag a prompt is still required.
	if err := runChatCmdStdin(t, dbPath, "hi", "--runtime", "claude"); err == nil {
		t.Error("no prompt at all must be refused")
	}
}

func TestPageReadRefusesToBeWidened(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, _, _, _ := writeCapturingMonomind(t)
	for _, args := range [][]string{
		{"--runtime", "claude", "--tools", "monoagent:read,runs", "--", "hi"},
		{"--runtime", "claude", "--tools", "monoagent:read", "--canvas", "draft", "--", "hi"},
	} {
		if _, err := runChatCmd(t, dbPath, bin, args...); err == nil {
			t.Errorf("%v must be refused", args)
		}
	}
}
