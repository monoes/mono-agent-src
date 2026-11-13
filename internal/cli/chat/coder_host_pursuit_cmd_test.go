package chat

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

func runPursuitVerb(t *testing.T, cfg *Deps, args ...string) (string, error) {
	t.Helper()
	cmd := newCoderHostPursuitCmd(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestPursuitVerbsWriteTheReservedRowsAndStatusCarriesTheNoticeAndPause(t *testing.T) {
	asOperator(t)
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
	cfg.RequireOperator = func(string) error { return nil }
	if _, err := runPursuitVerb(t, cfg, "off"); err != nil {
		t.Fatal(err)
	}
	if _, err := runPursuitVerb(t, cfg, "judge", "set", "openrouter/x:free"); err != nil {
		t.Fatal(err)
	}
	if _, err := runPursuitVerb(t, cfg, "judge", "set", "bad pin"); err == nil {
		t.Fatal("a pin that does not parse was saved")
	}
	out, err := runPursuitVerb(t, cfg, "status")
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		OperatorOff  bool   `json:"operatorOff"`
		SavedJudge   string `json:"savedJudge"`
		Notice       string `json:"notice"`
		PauseCommand string `json:"pauseCommand"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !st.OperatorOff || st.SavedJudge != "openrouter/x:free" || st.Notice != coderhost.PursuitNotice() || st.PauseCommand != coderhost.PursuitPauseCommand || !strings.Contains(st.Notice, "`"+st.PauseCommand+"`") {
		t.Fatalf("status = %+v", st)
	}
	if _, err := runPursuitVerb(t, cfg, "on"); err != nil {
		t.Fatal(err)
	}
	out, _ = runPursuitVerb(t, cfg, "status")
	if strings.Contains(out, `"operatorOff":true`) {
		t.Fatalf("`on` did not clear the explicit OFF: %s", out)
	}
}

func TestPursuitVerbsAreOperatorOnly(t *testing.T) {
	t.Setenv(coderhost.SessionTokenEnv, "x")
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
	cfg.RequireOperator = func(string) error { return nil }
	for _, args := range [][]string{{"on"}, {"off"}, {"judge", "set", "a/b"}, {"judge", "clear"}} {
		if _, err := runPursuitVerb(t, cfg, args...); err == nil {
			t.Errorf("%v ran inside an agent session", args)
		}
	}
}
