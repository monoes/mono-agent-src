package chat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/monomind"
)

const (
	fullWarningMark = "has full access"
	boxOffMark      = "not enforced because the coder box is off"
	anyCommandMark  = "run any command and change any file as you"
)

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

// A new chat that can run anything warns on stderr: full access, whether the
// level came from the default or a flag, and coding or workspace while the
// coder box is off (nothing then holds them to the level). stdout stays one
// JSON value.
func TestCreateWarnsWhenTheChatCanRunAnything(t *testing.T) {
	r := newAccessRig(t, true)
	for _, tc := range []struct {
		name, box  string
		args       []string
		full, held bool // which warning is due: full access, or the level not enforced
	}{
		{name: "default level", args: []string{"--new-workspace"}, full: true},
		{name: "--access full", args: []string{"--access", "full", "--new-workspace"}, full: true},
		{name: "--mode coder", args: []string{"--mode", "coder", "--new-workspace"}, full: true},
		{name: "full, box on", box: BoxCompat, args: []string{"--access", "full", "--new-workspace"}, full: true},
		{name: "coding, box unset", args: []string{"--access", "coding", "--new-workspace"}, held: true},
		{name: "coding, box off", box: BoxOff, args: []string{"--access", "coding", "--new-workspace"}, held: true},
		{name: "workspace, box unset", args: []string{"--access", "workspace", "--new-workspace"}, held: true},
		{name: "coding, box compat", box: BoxCompat, args: []string{"--access", "coding", "--new-workspace"}},
		{name: "coding, box strict", box: BoxStrict, args: []string{"--access", "coding", "--new-workspace"}},
		{name: "scoped", args: []string{"--access", "scoped"}},
	} {
		setCoderSettings(t, r.dbPath, coderSettings{Enabled: true, Box: tc.box})
		out, errOut := chatWithStderr(t, r.dbPath, append([]string{"history", "create", "--runtime", "claude"}, tc.args...)...)
		var rec ai.ConversationRecord
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil || rec.ID == "" {
			t.Errorf("%s: stdout is not the conversation JSON (%v): %q", tc.name, err, out)
		}
		if got := strings.Contains(errOut, fullWarningMark); got != tc.full {
			t.Errorf("%s: full-access warning = %v, want %v; stderr %q", tc.name, got, tc.full, errOut)
		}
		if got := strings.Contains(errOut, boxOffMark); got != tc.held {
			t.Errorf("%s: box-off warning = %v, want %v; stderr %q", tc.name, got, tc.held, errOut)
		}
		want := 0
		if tc.full || tc.held {
			want = 1
		}
		if got := strings.Count(errOut, anyCommandMark); got != want {
			t.Errorf("%s: %d warnings, want %d; stderr %q", tc.name, got, want, errOut)
		}
		if tc.held && !strings.Contains(errOut, "the "+rec.Access+" access level") {
			t.Errorf("%s: the level is not named: %q", tc.name, errOut)
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

// The first turn of a coding or workspace chat warns while the box is off, and
// a coding turn the box holds does not.
func TestFirstTurnBelowFullWarnsWhileTheBoxIsOff(t *testing.T) {
	for _, tc := range []struct {
		access, box string
		want        bool
	}{{ai.AccessCoding, "", true}, {ai.AccessWorkspace, "", true}, {ai.AccessCoding, BoxCompat, false}} {
		dbPath := fullAccessRig(t)
		setCoderSettings(t, dbPath, coderSettings{Enabled: true, BudgetUSD: 3, Box: tc.box})
		conv, err := openTestChatStore(t, dbPath).CreateConversationAccess("default", "agent", "general", "claude", "", "", tc.access, t.TempDir(), "")
		if err != nil {
			t.Fatal(err)
		}
		// A boxed turn needs bubblewrap, which this machine may lack: then only the silence matters.
		_, errOut, err := chatStreams(t, dbPath, "--conversation", conv.ID, "--turn", "turn-1", "--instance", "i", "--", "fix it")
		if tc.want && err != nil {
			t.Fatalf("%s: %v", tc.access, err)
		}
		if got := strings.Count(errOut, boxOffMark); (got == 1) != tc.want || got > 1 || strings.Contains(errOut, fullWarningMark) {
			t.Errorf("%s box %q: stderr %q, want warned %v", tc.access, tc.box, errOut, tc.want)
		}
		if !tc.want {
			continue
		}
		if _, errOut = chatWithStderr(t, dbPath, "--conversation", conv.ID, "--turn", "turn-2", "--instance", "i", "--", "more"); strings.Contains(errOut, anyCommandMark) {
			t.Errorf("%s: the second turn warned again: %q", tc.access, errOut)
		}
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
