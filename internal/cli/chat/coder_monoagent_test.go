package chat

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/cli/agent"
	"github.com/monoes/mono-agent/internal/monomind"
)

// minimalCoderTranscript is the shortest `agent exec` output that completes a
// coder turn and stores its session, so the next turn can resume it.
const minimalCoderTranscript = `  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"session","session_id":"th_guidance"}'
  echo '{"v":1,"type":"assistant","text":"done"}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"done"}'
  echo '{"v":1,"type":"done","exit_code":0}'`

// copyingSystemFile is a fake monomind `agent exec` body that copies the
// --system-file the turn passed to dst, which Exec deletes once the turn
// ends, and then plays transcript.
func copyingSystemFile(dst, transcript string) string {
	return `  prev=''
  for a in "$@"; do
    if [ "$prev" = "--system-file" ]; then cp "$a" '` + dst + `'; fi
    prev="$a"
  done
` + transcript
}

// Every coder turn tells the agent how to reach this monoagent installation,
// a resumed one included: the GUI runs each message as its own `chat`
// process, and a resumed session keeps the system prompt of its first turn
// only if the runtime says so. Dropping it from either leaves the agent
// building standalone code with no idea the nodes and workflows exist.
func TestCoderTurnTellsTheAgentHowToReachMonoagent(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	captured := filepath.Join(t.TempDir(), "system.md")
	bin, argsLog := writeCoderMonomind(t, copyingSystemFile(captured, minimalCoderTranscript))
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	cwd := t.TempDir()
	conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, cwd)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, turn := range []string{"turn-1", "turn-2"} {
		os.Remove(captured)
		if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", turn, "--instance", "i", "--", "post my RSS digest to Slack every morning"); err != nil {
			t.Fatalf("%s: %v\n%s", turn, err, out)
		}
		raw, err := os.ReadFile(captured)
		if err != nil {
			t.Fatalf("%s: the agent exec was given no --system-file: %v", turn, err)
		}
		prompt := string(raw)
		for _, want := range []string{
			"working in " + cwd,                     // the coder prompt it already had
			agent.ShellQuote(self),                  // this binary, by absolute path: the GUI may not have it on PATH
			"--profile default",                     // the turn's profile; the CLI ignores MONOAGENT_PROFILE
			"--db-path " + agent.ShellQuote(dbPath), // the turn's database, which isn't the default one
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s: system prompt lacks %q:\n%s", turn, want, prompt)
			}
		}
	}

	// The second turn really was a resume of the first one's session, so the
	// check above covered the resumed path.
	var execs []string
	logged, _ := os.ReadFile(argsLog)
	for _, l := range strings.Split(string(logged), "\n") {
		if strings.HasPrefix(l, "agent exec") {
			execs = append(execs, l)
		}
	}
	if len(execs) != 2 || strings.Contains(execs[0], "--resume") || !strings.Contains(execs[1], "--resume th_guidance") {
		t.Fatalf("want a fresh turn then a resume of th_guidance, got:\n%s", strings.Join(execs, "\n"))
	}
}

// The invocation names the binary by absolute path and pins the turn's
// profile and database: the agent's shell has none of the chat process's
// flags, and the CLI reads the active profile from settings when a call
// carries none, so a bare call would land in another profile or database.
func TestMonoagentInvocation(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		profile, db string
		want        string
	}{
		{"profile on the default database", "work", defaultDBPath, "/opt/m/monoagentcli --profile work"},
		{"no resolved profile", "", defaultDBPath, "/opt/m/monoagentcli"},
		{"no database path at all", "work", "", "/opt/m/monoagentcli --profile work"},
		{"another database", "work", "/data/alt.db", "/opt/m/monoagentcli --profile work --db-path /data/alt.db"},
		{"a home-relative database", "work", "~/alt/m.db", "/opt/m/monoagentcli --profile work --db-path " + filepath.Join(home, "alt", "m.db")},
	} {
		if got := monoagentInvocation("/opt/m/monoagentcli", tc.profile, tc.db); got != tc.want {
			t.Errorf("%s: invocation = %q, want %q", tc.name, got, tc.want)
		}
	}

	// os.Executable can fail, and an empty path must not reach the agent as ''.
	if got := monoagentInvocation("", "work", defaultDBPath); got != "monoagentcli --profile work" {
		t.Errorf("no binary path: invocation = %q, want the bare name, found on PATH", got)
	}

	// A relative path would resolve against the agent's folder, not the
	// chat process's, and find another database.
	got := monoagentInvocation("/opt/m/monoagentcli", "work", "rel/m.db")
	if _, db, ok := strings.Cut(got, "--db-path "); !ok || !filepath.IsAbs(db) {
		t.Errorf("a relative database path must be made absolute, got %q", got)
	}
}

// An app bundle's binary has spaces in its path, and so can the database:
// the invocation must reach the CLI as the same arguments through a shell.
func TestMonoagentInvocationSurvivesAShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs the invocation through sh")
	}
	bundle := filepath.Join(t.TempDir(), "Mono Agent.app", "Contents")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	argv := filepath.Join(t.TempDir(), "argv")
	fake := filepath.Join(bundle, "monoagentcli")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + argv + "'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	inv := monoagentInvocation(fake, "work", "/data/my dbs/m.db")
	if out, err := exec.Command("sh", "-c", inv+` --json workflow search "daily digest"`).CombinedOutput(); err != nil {
		t.Fatalf("running %q: %v\n%s", inv, err, out)
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	want := []string{"--profile", "work", "--db-path", "/data/my dbs/m.db", "--json", "workflow", "search", "daily digest"}
	if !slices.Equal(got, want) {
		t.Errorf("the CLI received %q, want %q", got, want)
	}
}
