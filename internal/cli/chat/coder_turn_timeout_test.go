package chat

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/monomind"
)

// `coder set --timeout` is the wall-clock cap of a message. It is read strictly, like --startup-timeout: "3mins" is
// not 3 seconds, and a bare number is not seconds.
func TestCoderSetTimeoutIsReadStrictly(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withCoderCaps(t)
	set := func(v string) (coderStatus, int, string) {
		t.Helper()
		out, code := runCoderCLI(t, dbPath, "set", "--timeout="+v)
		var st coderStatus
		if code == 0 {
			decodeChatJSON(t, out, &st)
		}
		return st, code, out
	}
	for _, c := range []struct{ in, stored string }{
		{"30m", "30m0s"}, {" 2h ", "2h0m0s"}, {"1.5h", "1h30m0s"}, {"1s", "1s"}, {"576h", "576h0m0s"},
		{"1000h", "576h0m0s"}, // monomind's timer overflows above about 596h and would end the turn at once
		{"45m", "45m0s"},
	} {
		if st, code, out := set(c.in); code != 0 || st.Timeout != c.stored {
			t.Errorf("--timeout=%q: exit %d, stored %q, want %q: %s", c.in, code, st.Timeout, c.stored, out)
		}
	}
	for _, bad := range []string{"", "0", "0s", "-1s", "-90", "500ms", "90", "1.5", "1e3", "3mins", "3 min", "30min", "2 hours",
		"1d", "1h30", "3m!", "soon", "inf", "10000000h"} {
		if _, code, out := set(bad); code != 3 || !strings.Contains(out, "--timeout") {
			t.Errorf("--timeout=%q accepted (exit %d): %s", bad, code, out)
		}
	}
	out, _ := runCoderCLI(t, dbPath, "status")
	var st coderStatus
	decodeChatJSON(t, out, &st)
	if st.Timeout != "45m0s" {
		t.Errorf("a refused value changed the setting: %q", st.Timeout)
	}
}

// A stored value that can't be read as a cap (a hand edit) counts as none: turns run under the default, the status
// says so, and the other settings can still be changed. A stored value above the maximum is cut to it.
func TestCoderStoredBadTimeout(t *testing.T) {
	for stored, want := range map[string]time.Duration{
		"": time.Hour, "3 min": time.Hour, "3mins": time.Hour, "90": time.Hour, "-5m": time.Hour, "0s": time.Hour,
		"500ms": time.Hour, "3s": 3 * time.Second, "90m": 90 * time.Minute, "1000h": 576 * time.Hour,
	} {
		if got := (coderSettings{Timeout: stored}).timeout(); got != want {
			t.Errorf("stored %q: turns get %s, want %s", stored, got, want)
		}
	}

	dbPath := newChatCLITestDB(t)
	withCoderCaps(t)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, Timeout: "3 min"})
	text := func() string {
		t.Helper()
		var out bytes.Buffer
		cmd := newCoderCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "default"}))
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"status"})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if want := `timeout: 1h0m0s (the default: the stored value "3 min" is not a duration of at least 1s`; !strings.Contains(text(), want) {
		t.Errorf("status text lacks %q:\n%s", want, text())
	}
	out, code := runCoderCLI(t, dbPath, "set", "--max-turns", "9")
	var st coderStatus
	decodeChatJSON(t, out, &st)
	if code != 0 || st.MaxTurns != 9 || st.Timeout != "3 min" {
		t.Errorf("another change on a bad stored timeout: exit %d %s", code, out)
	}
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, Timeout: "1000h"})
	if want := `timeout: 576h0m0s (the maximum: the stored value "1000h" is above it)`; !strings.Contains(text(), want) {
		t.Errorf("status text lacks %q:\n%s", want, text())
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--timeout", "20m"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := text(); !strings.Contains(got, "timeout: 20m0s\n") {
		t.Errorf("status text after a good value:\n%s", got)
	}
}

// The cap a turn runs under reaches the lead's and its workers' execs; an unreadable stored value is the default's.
func TestCoderTurnsGetTheReadableTimeout(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeOrgMonomind(t)
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, Timeout: "3mins"})
	conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "dynamic"); code != 0 {
		t.Fatalf("set-org: exit %d", code)
	}
	if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "t1", "--", "build it"); err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	execs := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "agent exec") {
			execs++
			if !strings.Contains(line, "--timeout 3600s") {
				t.Errorf("an exec lacks the default cap: %s", line)
			}
		}
	}
	if execs < 2 {
		t.Fatalf("%d execs, want the lead's and a worker's:\n%s", execs, raw)
	}
}
