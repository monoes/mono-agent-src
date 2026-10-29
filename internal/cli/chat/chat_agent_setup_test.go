package chat

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// notLoggedInTranscript is what monomind 2.16 streams for a Claude Code
// with no login: a non-fatal runner-error, then done 1.
const notLoggedInTranscript = `  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/app","pid":1}'
  echo '{"v":1,"type":"assistant","text":"Not logged in · Please run /login"}'
  echo '{"v":1,"type":"error","code":"runner-error","fatal":false,"message":"Claude Code returned an error result: Not logged in · Please run /login"}'
  echo '{"v":1,"type":"done","exit_code":1}'`

// withoutMonomind makes monomind undiscoverable: no override, an empty
// PATH and a HOME without any install root.
func withoutMonomind(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("PATH", t.TempDir())
	t.Setenv(monomind.EnvOverride, filepath.Join(t.TempDir(), "no-monomind"))
	if bin, err := monomind.Find(); err == nil {
		t.Skipf("monomind is installed system-wide at %s", bin)
	}
}

// runChatJSON is runChatCmd with --json: a failure ends stdout with the
// {"error","code"} object.
func runChatJSON(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	cmd := newChatCmd(cfg)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	realStdout := os.Stdout
	os.Stdout = w
	cmd.SetOut(w)
	execErr := cmd.Execute()
	os.Stdout = realStdout
	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String(), execErr
}

func lastJSONLine(t *testing.T, out string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var body map[string]any
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &body); err != nil {
		t.Fatalf("last stdout line is not JSON: %q", out)
	}
	return body
}

func TestChatJSONErrorWhenMonomindMissing(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withoutMonomind(t)
	out, err := runChatJSON(t, dbPath, "--runtime", "claude", "hi")
	if !monomind.IsNotFound(err) {
		t.Fatalf("err = %v, want monomind not found", err)
	}
	body := lastJSONLine(t, out)
	if body["code"] != monomind.AgentNotSetupCode || !strings.Contains(body["error"].(string), "monomind not found") {
		t.Errorf("error object = %v", body)
	}
}

func TestChatJSONErrorWhenNotLoggedIn(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	t.Setenv(monomind.EnvOverride, writeFakeMonomindForChat(t, notLoggedInTranscript))
	out, err := runChatJSON(t, dbPath, "--runtime", "claude", "hi")
	if exitCodeFor(err) != 1 {
		t.Errorf("exit %d, want the turn's 1", exitCodeFor(err))
	}
	if body := lastJSONLine(t, out); body["code"] != monomind.AgentNotSetupCode || !strings.Contains(body["error"].(string), "Not logged in") {
		t.Errorf("error object = %v", body)
	}
}

func TestJournaledTurnNotLoggedInCarriesCode(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	bin := writeFakeMonomindForChat(t, notLoggedInTranscript)
	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "t-login", "--", "hi")
	if !monomind.IsAgentNotSetup(err) {
		t.Fatalf("err = %v, want agent_not_setup", err)
	}
	p := parseJournaledTurn(t, out).finished(t)
	if p.Status != chatevents.StatusFailed || p.Code != monomind.AgentNotSetupCode || !strings.Contains(p.Reason, "Not logged in") {
		t.Errorf("turn.finished = %+v", p)
	}
	// The stored event carries the code too, for a reopened conversation.
	stored, _ := store.GetEvents(conv.ID, "t-login", "default", 0, 100)
	last := stored[len(stored)-1]
	if last.Type != chatevents.EventTurnFinished || !strings.Contains(string(last.Payload), `"code":"agent_not_setup"`) {
		t.Errorf("stored turn.finished = %s", last.Payload)
	}
}

func TestJournaledTurnWithoutMonomindCarriesCode(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversation("default", "agent", "general", "claude", "", "")
	withoutMonomind(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"})
	cmd := newChatCmd(cfg)
	cmd.SetArgs([]string{"--conversation", conv.ID, "--turn", "t-nomm", "--", "hi"})
	r, w, _ := os.Pipe()
	realStdout := os.Stdout
	os.Stdout = w
	err := cmd.Execute()
	os.Stdout = realStdout
	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	if !monomind.IsNotFound(err) {
		t.Fatalf("err = %v, want monomind not found", err)
	}
	if p := parseJournaledTurn(t, buf.String()).finished(t); p.Status != chatevents.StatusFailed || p.Code != monomind.AgentNotSetupCode {
		t.Errorf("turn.finished = %+v", p)
	}
}

func TestJournaledTurnOtherFailureHasNoCode(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversation("default", "agent", "general", "fake", "", "")
	bin := writeFakeMonomindForChat(t, `  echo '{"v":1,"type":"start","runtime":"fake","cwd":"/app","pid":1}'
  echo '{"v":1,"type":"error","code":"quota","fatal":true,"message":"usage limit reached"}'
  echo '{"v":1,"type":"done","exit_code":1}'`)
	out, _ := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "t-quota", "--", "hi")
	if p := parseJournaledTurn(t, out).finished(t); p.Status != chatevents.StatusFailed || p.Code != "" {
		t.Errorf("turn.finished = %+v, want failed without a code", p)
	}
}
