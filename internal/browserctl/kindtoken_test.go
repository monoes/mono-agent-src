package browserctl

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// asKind gives the test the environment a spawner issues for kind
// (IssueAgentKind), and removes the record when the test ends.
func asKind(t *testing.T, kind string) {
	t.Helper()
	env, _, done := IssueAgentKind(kind)
	t.Cleanup(done)
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
}

func writeKindRecord(t *testing.T, token string, rec kindRecord) {
	t.Helper()
	dir, err := KindTokenDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, token+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// deadPID is the pid of a process that has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// IssueAgentKind writes a private record naming this process and hands
// back the environment for the agents; done removes it, and issuing again
// prunes what a crashed spawner left.
func TestIssueAgentKindWritesARecord(t *testing.T) {
	env, _, done := IssueAgentKind(KindOrg)
	if len(env) != 2 || env[0] != EnvAgentKind+"=org" || !strings.HasPrefix(env[1], EnvAgentKindToken+"=") {
		t.Fatalf("env: %q", env)
	}
	token := strings.TrimPrefix(env[1], EnvAgentKindToken+"=")
	dir, _ := KindTokenDir()
	path := filepath.Join(dir, token+".json")
	if runtime.GOOS != "windows" { // Windows has no Unix modes
		if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("dir: %v %v", fi.Mode(), err)
		}
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("record: %v %v", fi.Mode(), err)
		}
	}
	if rec, ok := readKindRecord(path); !ok || rec.Kind != KindOrg || rec.PID != os.Getpid() || rec.Start == "" || !rec.live() {
		t.Errorf("record: %+v %v", rec, ok)
	}
	done()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("done left the record: %v", err)
	}

	dead, young, old := strings.Repeat("d", 32), strings.Repeat("a", 32), strings.Repeat("b", 32)
	writeKindRecord(t, dead, recordFor(KindOrg, deadPID(t), time.Now()))
	writeKindRecord(t, young, recordFor(KindOrg, os.Getpid(), time.Now().Add(-time.Hour)))
	writeKindRecord(t, old, recordFor(KindOrg, os.Getpid(), time.Now().Add(-8*24*time.Hour)))
	_, _, done2 := IssueAgentKind(KindCoder)
	defer done2()
	for _, tok := range []string{dead, old} {
		if _, err := os.Stat(filepath.Join(dir, tok+".json")); !os.IsNotExist(err) {
			t.Errorf("record %s not pruned: %v", tok[:4], err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, young+".json")); err != nil {
		t.Errorf("a live record was pruned: %v", err)
	}
	if env, _, _ := IssueAgentKind(KindChat); env != nil {
		t.Errorf("chat is never declared: %q", env)
	}
}

// A declared kind counts only with a token its spawner issued: the
// variable alone, a token for another kind, a removed record, a dead
// spawner or a token that is not one all leave a shell kind other.
func TestDeclaredKindNeedsAToken(t *testing.T) {
	actAsHuman(t)
	t.Setenv(EnvAICaller, "")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv(EnvAgentKind, KindOrg)
	t.Setenv(EnvAgentKindToken, "")
	if k := AgentKind(); k != KindOther {
		t.Fatalf("%s=org with no token: %q", EnvAgentKind, k)
	}
	t.Setenv(EnvAgentKind, "")
	t.Setenv("MONOMIND_ORG_ROLE", "growth")
	if k := AgentKind(); k != KindOther {
		t.Fatalf("MONOMIND_ORG_ROLE with no token: %q", k)
	}
	t.Setenv("MONOMIND_ORG_ROLE", "")

	env, _, done := IssueAgentKind(KindOrg)
	token := strings.TrimPrefix(env[1], EnvAgentKindToken+"=")
	t.Setenv(EnvAgentKind, KindOrg)
	t.Setenv(EnvAgentKindToken, token)
	if k := AgentKind(); k != KindOrg {
		t.Fatalf("a valid token: %q", k)
	}
	t.Setenv(EnvAgentKind, KindCoder)
	if k := AgentKind(); k != KindOther {
		t.Fatalf("an org token for coder: %q", k)
	}
	t.Setenv(EnvAgentKind, KindOrg)
	done()
	if k := AgentKind(); k != KindOther {
		t.Fatalf("the record removed: %q", k)
	}

	dead := strings.Repeat("d", 32)
	writeKindRecord(t, dead, recordFor(KindOrg, deadPID(t), time.Now()))
	t.Setenv(EnvAgentKindToken, dead)
	if k := AgentKind(); k != KindOther {
		t.Fatalf("a dead spawner: %q", k)
	}
	young, old := strings.Repeat("a", 32), strings.Repeat("b", 32)
	writeKindRecord(t, young, recordFor(KindOrg, os.Getpid(), time.Now().Add(-6*24*time.Hour)))
	writeKindRecord(t, old, recordFor(KindOrg, os.Getpid(), time.Now().Add(-8*24*time.Hour)))
	t.Setenv(EnvAgentKindToken, young)
	if k := AgentKind(); k != KindOrg {
		t.Fatalf("a live spawner, six days old: %q", k)
	}
	t.Setenv(EnvAgentKindToken, old)
	if k := AgentKind(); k != KindOther {
		t.Fatalf("a live spawner, eight days old: %q", k)
	}
	// The pid now belongs to another process (it started at another time).
	reused := strings.Repeat("c", 32)
	rec := recordFor(KindOrg, os.Getpid(), time.Now())
	rec.Start += "1"
	writeKindRecord(t, reused, rec)
	t.Setenv(EnvAgentKindToken, reused)
	if k := AgentKind(); k != KindOther {
		t.Fatalf("a reused pid: %q", k)
	}
	// A record with no start time (an older build's) never counts.
	rec.Start = ""
	writeKindRecord(t, reused, rec)
	if k := AgentKind(); k != KindOther {
		t.Fatalf("no start time: %q", k)
	}
	for _, bad := range []string{"../" + young, strings.ToUpper(young), young + "x"} {
		t.Setenv(EnvAgentKindToken, bad)
		if k := AgentKind(); k != KindOther {
			t.Fatalf("token %q: %q", bad, k)
		}
	}
}

// TestKindTokenSleeper is the child process TestBoundRecordFollowsTheChild
// starts: it only sleeps.
func TestKindTokenSleeper(t *testing.T) {
	if os.Getenv("BROWSERCTL_KIND_SLEEPER") != "1" {
		t.Skip("a helper process")
	}
	time.Sleep(time.Minute)
}

// A launcher that exits while what it started goes on (a detached org run)
// binds the record to the started process: it counts while that process
// runs, though the launcher is gone, and stops when it ends.
func TestBoundRecordFollowsTheChild(t *testing.T) {
	actAsHuman(t)
	t.Setenv(EnvAICaller, "")
	t.Setenv("CLAUDECODE", "1")
	env, bind, done := IssueAgentKind(KindOrg)
	defer done()
	t.Setenv(EnvAgentKind, KindOrg)
	t.Setenv(EnvAgentKindToken, strings.TrimPrefix(env[1], EnvAgentKindToken+"="))
	if k := AgentKind(); k != KindOrg {
		t.Fatalf("issued: %q", k)
	}
	// The launcher has exited: its record names a pid that is gone.
	dir, _ := KindTokenDir()
	path := filepath.Join(dir, strings.TrimPrefix(env[1], EnvAgentKindToken+"=")+".json")
	rec, _ := readKindRecord(path)
	rec.PID = deadPID(t)
	if err := writeKindRecordFile(dir, path, rec); err != nil {
		t.Fatal(err)
	}
	if k := AgentKind(); k != KindOther {
		t.Fatalf("an exited launcher's record still counts: %q", k)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestKindTokenSleeper$")
	child.Env = append(os.Environ(), "BROWSERCTL_KIND_SLEEPER=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	bind(child.Process.Pid)
	if k := AgentKind(); k != KindOrg {
		t.Fatalf("bound to a running child: %q", k)
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if k := AgentKind(); k != KindOther {
		t.Fatalf("the child has ended: %q", k)
	}
}

// A kind a shell declares but can't prove binds it at the stricter of that
// kind's level and other's: a spoof never gains, and a lost token (none,
// a dead spawner, records it can't read) never loosens.
func TestUnprovenKindFailsClosed(t *testing.T) {
	actAsHuman(t)
	t.Setenv(EnvAICaller, "")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv(EnvAgentKind, KindOrg)
	t.Setenv(EnvAgentKindToken, "")
	levels := func(org, other string) Permissions {
		p := DefaultPermissions()
		p.SetLevel(KindOrg, org)
		p.SetLevel(KindOther, other)
		return p
	}
	c := AgentCaller()
	if c != (Caller{Kind: KindOther, Declared: KindOrg}) {
		t.Fatalf("caller: %+v", c)
	}
	var off *BrowserOffError
	if err := levels(LevelOff, LevelDev).PolicyForCaller(c).Check("http://localhost:3000/"); !errors.As(err, &off) || off.Declared != KindOrg {
		t.Fatalf("org off, an unproven org shell: %v", err)
	}
	for _, tc := range []struct{ org, other, want string }{
		{LevelFull, LevelDev, LevelDev}, // the spoof gains nothing
		{LevelFullNoApprovals, LevelDev, LevelDev},
		{LevelDev, LevelFull, LevelDev}, // a lost token loosens nothing
		{LevelAsk, LevelDev, LevelDev},  // dev refuses a new site; ask would raise an approval
		{LevelDev, LevelAsk, LevelDev},
		{LevelOff, LevelFullNoApprovals, LevelOff},
	} {
		if got := levels(tc.org, tc.other).LevelOf(c); got != tc.want {
			t.Errorf("org %s, other %s: %s, want %s", tc.org, tc.other, got, tc.want)
		}
	}
	t.Setenv(EnvAgentKind, "")
	t.Setenv("MONOMIND_ORG_ROLE", "growth")
	if got := levels(LevelOff, LevelDev).LevelOf(AgentCaller()); got != LevelOff {
		t.Errorf("MONOMIND_ORG_ROLE unproven, org off: %s", got)
	}

	// Proven, the kind's own level applies.
	asKind(t, KindOrg)
	if c := AgentCaller(); c != (Caller{Kind: KindOrg}) || levels(LevelFull, LevelDev).LevelOf(c) != LevelFull {
		t.Fatalf("a vouched org shell: %+v", c)
	}
	// The records can't be read (a sandbox): unproven again.
	dir, _ := KindTokenDir()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if c := AgentCaller(); c != (Caller{Kind: KindOther, Declared: KindOrg}) || levels(LevelOff, LevelDev).LevelOf(c) != LevelOff {
		t.Fatalf("unreadable records: %+v", c)
	}
}

// processStart tells a running process by when it started, the same each
// time, and knows no process that has exited.
func TestProcessStart(t *testing.T) {
	a, ok := processStart(os.Getpid())
	if !ok || a == "" {
		t.Fatalf("this process: %q %v", a, ok)
	}
	if b, _ := processStart(os.Getpid()); b != a {
		t.Fatalf("not stable: %q, %q", a, b)
	}
	if s, ok := processStart(deadPID(t)); ok {
		t.Fatalf("an exited process: %q", s)
	}
	if _, ok := processStart(0); ok {
		t.Fatal("pid 0")
	}
}
