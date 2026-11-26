package chat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/monomind"
)

const fullWarningMark = "full access"

// chatStreams runs `chat <args>` as the operator and returns both streams and
// the command's error.
func chatStreams(t *testing.T, dbPath string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	var errBuf bytes.Buffer
	stdout = captureStdout(t, func() {
		cmd := newChatCmd(cfg)
		cmd.SetErr(&errBuf)
		cmd.SetArgs(args)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err = cmd.Execute()
	})
	return stdout, errBuf.String(), err
}

// chatWithStderr is chatStreams for a command that must succeed.
func chatWithStderr(t *testing.T, dbPath string, args ...string) (stdout, stderr string) {
	t.Helper()
	stdout, stderr, err := chatStreams(t, dbPath, args...)
	if err != nil {
		t.Fatalf("chat %v: %v\n%s\n%s", args, err, stdout, stderr)
	}
	return stdout, stderr
}

// fullAccessRig is a database and a fake monomind that runs coder turns.
func fullAccessRig(t *testing.T) string {
	t.Helper()
	dbPath := newChatCLITestDB(t)
	bin, _ := writeSandboxMonomind(t, "9.0.0", true, coderTranscript, monomind.CoderCapabilities...)
	t.Setenv(monomind.EnvOverride, bin)
	withCoderCaps(t, append([]string{monomind.CapAgentExecSandbox}, monomind.CoderCapabilities...)...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, BudgetUSD: 3})
	return dbPath
}

// A new chat at full access warns on stderr, whether the level came from the
// default or from a flag; stdout stays one JSON value.
func TestCreateAtFullAccessWarns(t *testing.T) {
	r := newAccessRig(t, true)
	setCoderSettings(t, r.dbPath, coderSettings{Enabled: true})
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"default level", []string{"--new-workspace"}, true},
		{"--access full", []string{"--access", "full", "--new-workspace"}, true},
		{"--mode coder", []string{"--mode", "coder", "--new-workspace"}, true},
		{"coding", []string{"--access", "coding", "--new-workspace"}, false},
		{"workspace", []string{"--access", "workspace", "--new-workspace"}, false},
		{"scoped", []string{"--access", "scoped"}, false},
	} {
		out, errOut := chatWithStderr(t, r.dbPath, append([]string{"history", "create", "--runtime", "claude"}, tc.args...)...)
		var rec ai.ConversationRecord
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil || rec.ID == "" {
			t.Errorf("%s: stdout is not the conversation JSON (%v): %q", tc.name, err, out)
		}
		if got := strings.Contains(errOut, fullWarningMark); got != tc.want {
			t.Errorf("%s: warned = %v, want %v; stderr %q", tc.name, got, tc.want, errOut)
		}
	}
}

// The first turn of a full-access chat warns; later turns do not.
func TestFirstTurnAtFullAccessWarns(t *testing.T) {
	dbPath := fullAccessRig(t)
	conv, err := openTestChatStore(t, dbPath).CreateConversationAccess("default", "agent", "general", "claude", "", "", ai.AccessFull, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	out, errOut := chatWithStderr(t, dbPath, "--conversation", conv.ID, "--turn", "turn-1", "--instance", "i", "--", "fix it")
	parseJournaledTurn(t, out) // stdout is still only JSON lines
	if strings.Count(errOut, fullWarningMark) != 1 {
		t.Errorf("first full turn: stderr %q", errOut)
	}
	if _, errOut = chatWithStderr(t, dbPath, "--conversation", conv.ID, "--turn", "turn-2", "--instance", "i", "--", "more"); strings.Contains(errOut, fullWarningMark) {
		t.Errorf("second turn warned again: %q", errOut)
	}
}

// A first turn below full says nothing about full access.
func TestFirstTurnBelowFullDoesNotWarnFull(t *testing.T) {
	dbPath := fullAccessRig(t)
	conv, err := openTestChatStore(t, dbPath).CreateConversationAccess("default", "agent", "general", "claude", "", "", ai.AccessCoding, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	// The level needs a box this machine may not have: only the warning matters.
	_, errOut, _ := chatStreams(t, dbPath, "--conversation", conv.ID, "--turn", "turn-1", "--instance", "i", "--", "fix it")
	if strings.Contains(errOut, fullWarningMark) {
		t.Errorf("a coding turn warned about full access: %q", errOut)
	}
}

// `chat --mode coder` is a new full-access chat.
func TestPlainCoderTurnAtFullAccessWarns(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, _ := writeCoderMonomind(t, coderTranscript)
	t.Setenv(monomind.EnvOverride, bin)
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	_, errOut := chatWithStderr(t, dbPath, "--mode", "coder", "--runtime", "claude", "--cwd", t.TempDir(), "--", "fix it")
	if !strings.Contains(errOut, fullWarningMark) {
		t.Errorf("plain coder turn did not warn: %q", errOut)
	}
}
