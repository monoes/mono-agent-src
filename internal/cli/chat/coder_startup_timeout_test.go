package chat

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/monomind"
)

// `coder set --startup-timeout` is how long claude may take to initialize in a coder turn (#1754): stored, shown
// with where the effective value comes from, cleared with 0, and overridden by the environment variable.
func TestCoderStartupTimeoutSetting(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withCoderCaps(t)
	t.Setenv(monomind.StartupTimeoutEnv, "")
	status := func(args ...string) coderStatus {
		t.Helper()
		out, code := runCoderCLI(t, dbPath, args...)
		if code != 0 {
			t.Fatalf("coder %v: exit %d %s", args, code, out)
		}
		var st coderStatus
		decodeChatJSON(t, out, &st)
		return st
	}
	check := func(st coderStatus, stored, effective, source string) {
		t.Helper()
		if st.StartupTimeout != stored || st.StartupTimeoutEffective != effective || st.StartupTimeoutSource != source {
			t.Errorf("stored %q, effective %q from %q; want %q, %q, %q",
				st.StartupTimeout, st.StartupTimeoutEffective, st.StartupTimeoutSource, stored, effective, source)
		}
	}
	check(status("status"), "", "2m0s", "default")
	check(status("set", "--startup-timeout", "3m"), "3m0s", "3m0s", "set")
	check(status("set", "--max-turns", "9"), "3m0s", "3m0s", "set") // another change keeps it

	var text bytes.Buffer
	cmd := newCoderCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}))
	cmd.SetOut(&text)
	cmd.SetArgs([]string{"status"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := "claude start-up limit of a coder turn: 3m0s (set by `coder set --startup-timeout`)"; !strings.Contains(text.String(), want) {
		t.Errorf("status text lacks %q:\n%s", want, text.String())
	}

	t.Setenv(monomind.StartupTimeoutEnv, "4m")
	check(status("status"), "3m0s", "4m0s", "env")
	t.Setenv(monomind.StartupTimeoutEnv, "")

	for _, bad := range []string{"", "-1s", "500ms", "soon"} {
		if out, code := runCoderCLI(t, dbPath, "set", "--startup-timeout="+bad); code != 3 {
			t.Errorf("--startup-timeout=%q accepted (exit %d): %s", bad, code, out)
		}
	}
	check(status("status"), "3m0s", "3m0s", "set")
	check(status("set", "--startup-timeout", "0"), "", "2m0s", "default")
}

// The stored limit reaches the lead's turn and its workers' turns; the environment variable overrides it.
func TestCoderTurnsGetTheStartupTimeoutSetting(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv(monomind.StartupTimeoutEnv, "")
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeOrgMonomind(t)
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, StartupTimeout: "3m0s"})
	conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "dynamic"); code != 0 {
		t.Fatalf("set-org: exit %d", code)
	}
	execs := func(turn string) []string {
		t.Helper()
		if err := os.WriteFile(argsLog, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", turn, "--", "build it"); err != nil {
			t.Fatalf("turn %s: %v\n%s", turn, err, out)
		}
		raw, err := os.ReadFile(argsLog)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "agent exec") {
				got = append(got, line)
			}
		}
		if len(got) < 2 {
			t.Fatalf("turn %s ran %d execs, want the lead's and a worker's:\n%s", turn, len(got), raw)
		}
		return got
	}
	for _, line := range execs("t1") {
		if !strings.Contains(line, "--startup-timeout 180s") {
			t.Errorf("an exec lacks the stored limit: %s", line)
		}
	}
	t.Setenv(monomind.StartupTimeoutEnv, "4m")
	for _, line := range execs("t2") {
		if !strings.Contains(line, "--startup-timeout 240s") {
			t.Errorf("an exec ignores the environment override: %s", line)
		}
	}
}
