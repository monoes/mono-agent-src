package chat

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/storage"
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
		OperatorOff  bool   `json:"savedOperatorOff"`
		SavedJudge   string `json:"savedJudge"`
		Notice       string `json:"notice"`
		PauseCommand string `json:"pauseCommand"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if !st.OperatorOff || st.SavedJudge != "openrouter/x:free" || st.Notice != coderhost.PursuitNotice(0) || st.PauseCommand != coderhost.PursuitPauseCommand || !strings.Contains(st.Notice, "`"+st.PauseCommand+" <session-id>`") {
		t.Fatalf("status = %+v", st)
	}
	if _, err := runPursuitVerb(t, cfg, "on"); err != nil {
		t.Fatal(err)
	}
	out, _ = runPursuitVerb(t, cfg, "status")
	if strings.Contains(out, `"savedOperatorOff":true`) {
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

func TestPursuitStatusWithNoHostReadsOnePlace(t *testing.T) {
	asOperator(t)
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
	cfg.RequireOperator = func(string) error { return nil }
	out, err := runPursuitVerb(t, cfg, "status")
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	if st["savedOperatorOff"] != false || st["missing"] != "judge-pin" || st["pinSource"] != nil || st["enabled"] != false ||
		st["pendingRestart"] != false || st["readError"] != nil || !strings.Contains(st["reason"].(string), "no running coder host") {
		t.Fatalf("status = %v", st)
	}
	if _, err := runPursuitVerb(t, cfg, "judge", "set", "openrouter/x:free"); err != nil {
		t.Fatal(err)
	}
	out, _ = runPursuitVerb(t, cfg, "status")
	st = nil
	_ = json.Unmarshal([]byte(out), &st)
	if st["missing"] != "" || st["pinSource"] != "saved" {
		t.Fatalf("with a saved pin and no host: %v", st)
	}
}

func TestPursuitStatusDoesNotReportAnUnreadableRowAsAChoice(t *testing.T) {
	asOperator(t)
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
	db, err := cfg.OpenDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec("ALTER TABLE configs RENAME TO configs_gone"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	cfg.OpenDB = func() (*storage.Database, error) { return storage.NewDatabase(clicore.ExpandPath(cfg.DBPath())) }
	out, err := runPursuitVerb(t, cfg, "status")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var st map[string]any
	_ = json.Unmarshal([]byte(out), &st)
	if st["savedOperatorOff"] != nil || st["readError"] == nil || st["pendingRestart"] != false {
		t.Fatalf("status = %v", st)
	}
}

func TestJudgeSetSaysWhenTheEnvPinOverridesIt(t *testing.T) {
	asOperator(t)
	t.Setenv(envPursuitJudge, "claude/haiku")
	cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
	cfg.RequireOperator = func(string) error { return nil }
	out, err := runPursuitVerb(t, cfg, "judge", "set", "openrouter/x:free")
	if err != nil || !strings.Contains(out, "envOverride") || !strings.Contains(out, "ignores the saved one") {
		t.Fatalf("%v: %s", err, out)
	}
}

// host.json is display-only and any process of the user can write it: a forged one is never shown as a fact.
func forgeHostJSON(t *testing.T, cfg *Deps, alive bool, pursuit string) {
	t.Helper()
	dir := coderhost.HostDir(clicore.ExpandPath(cfg.DBPath()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, coderhost.HostInfoFile), []byte(`{"pursuit":`+pursuit+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if alive {
		release, err := daemonhb.LockFile(filepath.Join(dir, "host.lock"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(release)
	}
}

func statusOf(t *testing.T, cfg *Deps) map[string]any {
	t.Helper()
	out, err := runPursuitVerb(t, cfg, "status")
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var st map[string]any
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStatusIgnoresAForgedHostJSON(t *testing.T) {
	good := `{"enabled":true,"pin":"claude/haiku","wakeCapUsd":0.01,"pinSource":"env"}`
	for name, c := range map[string]struct {
		alive   bool
		pursuit string
		trusted bool
	}{
		"dead host, forged enabled with a tiny cap": {false, good, false},
		"live host, bad cap":                        {true, `{"enabled":true,"pin":"claude/haiku","wakeCapUsd":1e9}`, false},
		"live host, bidi in the reason":             {true, `{"enabled":false,"reason":"ok ‮evil"}`, false},
		"live host, unknown missing code":           {true, `{"missing":"anything"}`, false},
		"live host, long reason":                    {true, `{"reason":"` + strings.Repeat("x", 700) + `"}`, false},
		"live host, sane facts":                     {true, good, true},
	} {
		t.Run(name, func(t *testing.T) {
			asOperator(t)
			cfg := testDeps(&testConfig{DBPath: newChatCLITestDB(t), ProfileID: "default", JSONOutput: true})
			forgeHostJSON(t, cfg, c.alive, c.pursuit)
			st := statusOf(t, cfg)
			if st["hostTrusted"] != c.trusted {
				t.Fatalf("hostTrusted = %v, want %v: %v", st["hostTrusted"], c.trusted, st)
			}
			notice := st["notice"].(string)
			if !c.trusted {
				if st["enabled"] != false || !strings.Contains(notice, "$2.00") || strings.Contains(notice, "$0.01") || !strings.Contains(st["reason"].(string), "no running coder host") {
					t.Fatalf("an untrusted host.json was shown: %v", st)
				}
			} else if st["enabled"] != true || !strings.Contains(notice, "$0.01") {
				t.Fatalf("a sane live host was not shown: %v", st)
			}
		})
	}
}
