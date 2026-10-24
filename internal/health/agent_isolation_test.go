package health

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const stateDir = `C:\Users\me\.monoagent`

// icacls prints the path on the first entry's line and indents the rest; the closing summary is localized.
func icaclsOut(entries ...string) string {
	var b strings.Builder
	for i, e := range entries {
		if i == 0 {
			b.WriteString(stateDir + " " + e + "\r\n")
		} else {
			b.WriteString(strings.Repeat(" ", len(stateDir)+1) + e + "\r\n")
		}
	}
	return b.String() + "\r\nVerarbeitete Dateien: 1; Fehler bei 0 Dateien\r\n"
}

func TestJudgeACLStatusMapping(t *testing.T) {
	base := []string{`NT AUTHORITY\SYSTEM:(OI)(CI)(F)`, `BUILTIN\Administrators:(OI)(CI)(F)`, `HOST\me:(OI)(CI)(F)`}
	cases := []struct {
		name    string
		account string
		extra   []string
		out     string
		err     error
		want    Status
	}{
		{name: "no entry for the account", account: "agent", extra: nil, want: StatusOK},
		{name: "other account only", account: "agent", extra: []string{`HOST\other:(F)`}, want: StatusOK},
		{name: "read entry", account: "agent", extra: []string{`HOST\agent:(OI)(CI)(RX)`}, want: StatusWarn},
		{name: "read by name only", account: "agent", extra: []string{`agent:(R)`}, want: StatusWarn},
		{name: "full entry", account: "agent", extra: []string{`HOST\agent:(OI)(CI)(F)`}, want: StatusFail},
		{name: "modify entry", account: "agent", extra: []string{`HOST\agent:(M)`}, want: StatusFail},
		{name: "specific write rights", account: "agent", extra: []string{`HOST\agent:(DE,RD,WD,AD)`}, want: StatusFail},
		{name: "case and dot domain", account: `.\AGENT`, extra: []string{`host\agent:(R)`}, want: StatusWarn},
		{name: "matching domain", account: `HOST\agent`, extra: []string{`HOST\agent:(R)`}, want: StatusWarn},
		{name: "other domain is another account", account: `OTHER\agent`, extra: []string{`HOST\agent:(F)`}, want: StatusOK},
		{name: "users group read", account: "agent", extra: []string{`BUILTIN\Users:(OI)(CI)(RX)`}, want: StatusWarn},
		{name: "everyone full", account: "agent", extra: []string{`Everyone:(F)`}, want: StatusFail},
		{name: "authenticated users by SID", account: "agent", extra: []string{`*S-1-5-11:(M)`}, want: StatusFail},
		{name: "deny entry is not access", account: "agent", extra: []string{`HOST\agent:(DENY)(F)`}, want: StatusOK},
		{name: "no access entry", account: "agent", extra: []string{`HOST\agent:(N)`}, want: StatusOK},
		{name: "inheritance flags alone", account: "agent", extra: []string{`HOST\agent:(OI)(CI)(IO)`}, want: StatusOK},
		{name: "highest of several", account: "agent", extra: []string{`HOST\agent:(R)`, `Everyone:(W)`}, want: StatusFail},
		{name: "empty output", account: "agent", out: "  \n", want: StatusWarn},
		{name: "summary line only", account: "agent", out: "Successfully processed 1 files; Failed processing 0 files\n", want: StatusWarn},
		{name: "command error", account: "agent", err: errors.New("exit status 5"), out: "Access is denied.", want: StatusWarn},
	}
	for _, c := range cases {
		out := c.out
		if out == "" && c.err == nil {
			out = icaclsOut(append(append([]string{}, base...), c.extra...)...)
		}
		got := judgeACL(c.account, stateDir, out, c.err)
		if got.Status != c.want {
			t.Errorf("%s: status %s, want %s (%s)", c.name, got.Status, c.want, got.Summary)
		}
		if c.want == StatusOK && !strings.Contains(got.Detail, "by hand") {
			t.Errorf("%s: an OK verdict must say what it cannot see: %q", c.name, got.Detail)
		}
	}
}

func TestParseICACLSKeepsPrincipalsWithSpaces(t *testing.T) {
	aces := parseICACLS(icaclsOut(`NT AUTHORITY\SYSTEM:(OI)(CI)(F)`, `HOST\agent 2:(R)`), stateDir)
	if len(aces) != 2 || aces[0].principal != `NT AUTHORITY\SYSTEM` || aces[1].principal != `HOST\agent 2` || aces[1].level != aclRead {
		t.Fatalf("%+v", aces)
	}
}

func fakeProbe(goos string, tools map[string]bool, files map[string]string, run func(string, ...string) ([]byte, error)) isolationProbe {
	return isolationProbe{
		goos: goos,
		lookPath: func(n string) (string, error) {
			if tools[n] {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		readFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v + "\n"), nil
			}
			return nil, errors.New("no such file")
		},
		run: func(_ context.Context, n string, a ...string) ([]byte, error) { return run(n, a...) },
	}
}

func withProbe(t *testing.T, p isolationProbe) {
	old := agentIsolationProbe
	agentIsolationProbe = p
	t.Cleanup(func() { agentIsolationProbe = old })
}

func TestAgentIsolationLinuxProbes(t *testing.T) {
	never := func(string, ...string) ([]byte, error) { t.Fatal("nothing should run"); return nil, nil }
	cases := []struct {
		name  string
		tools map[string]bool
		files map[string]string
		run   func(string, ...string) ([]byte, error)
		want  []string
	}{
		{"no bwrap, namespaces on", nil, map[string]string{"/proc/sys/user/max_user_namespaces": "63000"}, never,
			[]string{"bwrap was not found", "namespaces work"}},
		{"no bwrap, max 0", nil, map[string]string{"/proc/sys/user/max_user_namespaces": "0"}, never,
			[]string{"namespaces do not work", "max_user_namespaces is 0"}},
		{"no bwrap, debian switch", nil, map[string]string{"/proc/sys/kernel/unprivileged_userns_clone": "0"}, never,
			[]string{"unprivileged_userns_clone is 0"}},
		{"no bwrap, apparmor restriction", nil, map[string]string{"/proc/sys/kernel/apparmor_restrict_unprivileged_userns": "1"}, never,
			[]string{"apparmor_restrict_unprivileged_userns is 1"}},
		{"bwrap works", map[string]bool{"bwrap": true}, nil,
			func(string, ...string) ([]byte, error) { return nil, nil },
			[]string{"bwrap is present", "namespaces work"}},
		{"bwrap refused", map[string]bool{"bwrap": true}, nil,
			func(string, ...string) ([]byte, error) { return []byte("denied"), errors.New("exit status 1") },
			[]string{"bwrap is present", "namespaces do not work"}},
	}
	for _, c := range cases {
		withProbe(t, fakeProbe("linux", c.tools, c.files, c.run))
		res := checkAgentIsolation(context.Background(), &Env{})
		if res.Status != StatusInfo {
			t.Errorf("%s: status %s, an absent piece is never a warning", c.name, res.Status)
		}
		for _, w := range c.want {
			if !strings.Contains(res.Summary, w) {
				t.Errorf("%s: %q lacks %q", c.name, res.Summary, w)
			}
		}
	}
}

func TestAgentIsolationDarwinAndWindowsWithoutAccount(t *testing.T) {
	never := func(string, ...string) ([]byte, error) { t.Fatal("nothing should run"); return nil, nil }
	withProbe(t, fakeProbe("darwin", map[string]bool{"sandbox-exec": true}, nil, never))
	if res := checkAgentIsolation(context.Background(), &Env{}); res.Status != StatusInfo || !strings.Contains(res.Summary, "sandbox-exec is present") {
		t.Errorf("darwin present: %+v", res)
	}
	withProbe(t, fakeProbe("darwin", nil, nil, never))
	if res := checkAgentIsolation(context.Background(), &Env{}); res.Status != StatusInfo || !strings.Contains(res.Summary, "not found") {
		t.Errorf("darwin absent: %+v", res)
	}
	withProbe(t, fakeProbe("windows", nil, nil, never))
	res := checkAgentIsolation(context.Background(), &Env{DataDir: stateDir})
	if res.Status != StatusInfo || !strings.Contains(res.Summary, "not configured") || !strings.Contains(res.Summary, AgentIsolationDoc) {
		t.Errorf("windows without an account: %+v", res)
	}
	if strings.Contains(strings.ToLower(res.Summary+res.Detail), "http") {
		t.Errorf("the hint must name a command, not a URL: %+v", res)
	}
}

func TestAgentIsolationWindowsRunsIcaclsOnTheStateDir(t *testing.T) {
	var gotName string
	var gotArgs []string
	withProbe(t, fakeProbe("windows", nil, nil, func(n string, a ...string) ([]byte, error) {
		gotName, gotArgs = n, a
		return []byte(icaclsOut(`HOST\me:(F)`, `HOST\agent:(R)`)), nil
	}))
	res := checkAgentIsolation(context.Background(), &Env{DataDir: stateDir, AgentAccount: "agent"})
	if gotName != "icacls" || len(gotArgs) != 1 || gotArgs[0] != stateDir {
		t.Errorf("ran %s %v", gotName, gotArgs)
	}
	if res.Status != StatusWarn {
		t.Errorf("%+v", res)
	}
}

func TestAgentIsolationIsRegisteredAndNeverRequired(t *testing.T) {
	for _, c := range Default().Checks() {
		if c.ID == CheckAgentIsolation {
			if c.Required || c.Network {
				t.Errorf("must be optional and offline: %+v", c)
			}
			return
		}
	}
	t.Fatal("agent isolation check is not registered")
}
