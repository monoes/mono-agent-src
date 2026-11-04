package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A coder turn has full access to this computer: one that a workflow's
// process or the chat's runs start is refused (exit 3) before monomind runs,
// a plain turn and a coder conversation's alike. An agent's shell and the
// operator still start one (#831).
func TestCoderTurnsRefuseWorkflowAndChatRuns(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeCoderMonomind(t, promptCapturingTranscript(filepath.Join(t.TempDir(), "prompt")))
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	for _, k := range append(browserctl.AgentContextEnv(), workflow.EnvWorkflowRun, browserctl.EnvAICaller) {
		t.Setenv(k, "")
	}
	cwd := t.TempDir()
	conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, cwd)
	if err != nil {
		t.Fatal(err)
	}
	ran := func() bool {
		logged, _ := os.ReadFile(argsLog)
		return strings.Contains(string(logged), "agent exec")
	}
	for i, env := range [][2]string{{workflow.EnvWorkflowRun, "exec-7"}, {browserctl.EnvAICaller, "chat"}} {
		t.Setenv(env[0], env[1])
		for name, args := range map[string][]string{
			"a plain turn":          {"--mode", "coder", "--cwd", cwd, "--", "go on"},
			"a conversation's turn": {"--conversation", conv.ID, "--turn", "t-refused-" + string(rune('a'+i)), "--", "go on"},
		} {
			out, err := runChatCmd(t, dbPath, bin, args...)
			if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "can't start a coder chat ("+env[0]+" is set)") ||
				!strings.Contains(err.Error(), "Start it from the app or your own terminal") || !strings.Contains(err.Error(), "ask the operator to queue one") || !strings.Contains(err.Error(), "coder queue add") || !strings.Contains(err.Error(), "in their own terminal") ||
				!strings.Contains(err.Error(), "inherited this marker: restart it from your own terminal or the desktop launcher") {
				t.Errorf("%s under %s: %v\n%s", name, env[0], err, out)
			}
		}
		t.Setenv(env[0], "")
	}
	if ran() {
		t.Fatal("monomind ran a refused coder turn")
	}

	t.Setenv("CLAUDECODE", "1")
	if out, err := runChatCmd(t, dbPath, bin, "--mode", "coder", "--cwd", cwd, "--", "go on"); err != nil {
		t.Fatalf("an agent's shell: %v\n%s", err, out)
	}
	t.Setenv("CLAUDECODE", "")
	if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "t-mine", "--", "go on"); err != nil {
		t.Fatalf("the operator: %v\n%s", err, out)
	}
	if !ran() {
		t.Error("monomind never ran")
	}
}
