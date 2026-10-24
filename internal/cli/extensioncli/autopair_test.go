package extensioncli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
)

func seedPaired(t *testing.T, home string, list ...extension.PairedBrowser) {
	t.Helper()
	dir := filepath.Join(home, ".monoagent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(list)
	if err := os.WriteFile(filepath.Join(dir, "extension.browsers.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBrowsersListAndRevoke(t *testing.T) {
	pairTestHome(t)
	home, _ := os.UserHomeDir()
	seedPaired(t, home,
		extension.PairedBrowser{ID: "aaaa1111bbbb2222", Label: "Edge Work", Origin: "chrome-extension://x", Agent: "Edg/140", PairedAt: time.Now()},
		extension.PairedBrowser{ID: "cccc3333dddd4444", Label: "Chrome Home", Origin: "chrome-extension://x", PairedAt: time.Now()})
	d := testDeps(false)

	out, err := runExt(t, d, "browsers", "list")
	if err != nil || !strings.Contains(out, "Edge Work") || !strings.Contains(out, "Chrome Home") {
		t.Fatalf("list = %q, %v", out, err)
	}
	if strings.Contains(out, "tokenHash") {
		t.Fatalf("list shows token material: %q", out)
	}
	if _, err := runExt(t, d, "browsers", "revoke", "aaa"); err == nil {
		t.Fatal("a 3-character prefix should be refused")
	}
	out, err = runExt(t, d, "browsers", "revoke", "aaaa")
	if err != nil || !strings.Contains(out, "Revoked aaaa1111bbbb2222") {
		t.Fatalf("revoke = %q, %v", out, err)
	}
	if left, _ := extension.ListPairedBrowsers(); len(left) != 1 || left[0].Label != "Chrome Home" {
		t.Fatalf("left = %+v", left)
	}
	if _, err := runExt(t, d, "browsers", "revoke", "aaaa"); err == nil {
		t.Fatal("revoking a revoked browser should fail")
	}
}

// A marked process (an agent's shell, a workflow run) cannot revoke.
func TestBrowsersRevokeIsOperatorOnly(t *testing.T) {
	pairTestHome(t)
	home, _ := os.UserHomeDir()
	seedPaired(t, home, extension.PairedBrowser{ID: "aaaa1111bbbb2222", Label: "Edge Work", PairedAt: time.Now()})
	t.Setenv("CLAUDECODE", "1")
	d := testDeps(false)
	d.Refusal = func() string { return "an agent shell" }
	_, err := runExt(t, d, "browsers", "revoke", "aaaa")
	if !operatorOnlyErr(err) {
		t.Fatalf("revoke from a marked process = %v, want operator_only", err)
	}
	if left, _ := extension.ListPairedBrowsers(); len(left) != 1 {
		t.Fatalf("a refused revoke removed the browser: %+v", left)
	}
}

func TestExtensionPairModeReadsSetting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, "monoagent.db")
	if got := extensionPairMode(path)(); got != extension.PairModeAsk {
		t.Fatalf("no database = %q, want ask", got)
	}
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if got := extensionPairMode(path)(); got != extension.PairModeAsk {
		t.Fatalf("no entry = %q, want ask", got)
	}
	for v, want := range map[string]string{"false": "off", "FALSE": "off", "0": "off", "off": "off", " no ": "off", "auto": "auto", "ask": "ask", "on": "ask", "true": "ask", "junk": "ask"} {
		_ = db.SetSetting(autoPairSetting, v)
		if got := extensionPairMode(path)(); got != want {
			t.Errorf("extension.autoPair=%q = %q, want %q", v, got, want)
		}
	}
}

func TestAutoPairCommandSetsTheMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(home, "monoagent.db")
	d := testDepsDB(false, path)
	out, err := runExt(t, d, "autopair")
	if err != nil || !strings.Contains(out, "asks you") {
		t.Fatalf("autopair = %q, %v", out, err)
	}
	if out, err = runExt(t, d, "autopair", "auto"); err != nil || !strings.Contains(out, "automatic") {
		t.Fatalf("autopair auto = %q, %v", out, err)
	}
	if got := extensionPairMode(path)(); got != "auto" {
		t.Fatalf("the bridge's reader sees %q", got)
	}
	if out, err = runExt(t, d, "autopair", "off"); err != nil || !strings.Contains(out, "is off") {
		t.Fatalf("autopair off = %q, %v", out, err)
	}
	for _, bad := range []string{"maybe", "on"} {
		if _, err := runExt(t, d, "autopair", bad); err == nil {
			t.Fatalf("%q should be refused", bad)
		}
	}
	d.Refusal = func() string { return "an agent shell" }
	if _, err := runExt(t, d, "autopair", "auto"); !operatorOnlyErr(err) {
		t.Fatalf("an agent switching to auto = %v, want operator_only", err)
	}
	if got := extensionPairMode(path)(); got != "off" {
		t.Fatalf("a refused change moved the mode to %q", got)
	}
}

func newClaim(t *testing.T, label string, instance string) (extension.PendingClaim, string) {
	t.Helper()
	c, secret, err := extension.CreatePendingClaim(extension.PendingClaim{Label: label, Origin: "chrome-extension://" + strings.ToLower(label[:1]) + "aaa", Instance: instance})
	if err != nil {
		t.Fatal(err)
	}
	return c, extension.PairCode(c.ID, secret)
}

func pairHome(t *testing.T) *Deps {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(extension.AutoPairEnv, "")
	return testDepsDB(false, filepath.Join(home, "monoagent.db"))
}

func TestExtensionApproveAndDeny(t *testing.T) {
	d := pairHome(t)
	a, aCode := newClaim(t, "Edge", "instance-edge-1")
	b, bCode := newClaim(t, "Chrome", "instance-chrome-1")

	// With several waiting there is no "latest": the newest is the forger's to choose.
	for _, args := range [][]string{{"approve", "--code", bCode}, {"approve", "--latest", "--code", bCode}, {"deny"}} {
		if out, err := runExt(t, d, args...); err == nil {
			t.Fatalf("%v with two waiting = %q, want a refusal", args, out)
		} else if len(args) == 3 && args[1] == "--code" && (!strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), b.ID)) {
			t.Fatalf("the refusal does not list the waiting requests: %v", err)
		}
	}
	// An agent's shell cannot approve; the claim stays waiting.
	d.Refusal = func() string { return "an agent shell" }
	if _, err := runExt(t, d, "approve", b.ID, "--code", bCode); !operatorOnlyErr(err) {
		t.Fatalf("approve from a marked process = %v, want operator_only", err)
	}
	if _, err := runExt(t, d, "deny", a.ID); !operatorOnlyErr(err) {
		t.Fatalf("deny from a marked process = %v, want operator_only", err)
	}
	if _, err := runExt(t, d, "deny", "--all"); !operatorOnlyErr(err) {
		t.Fatalf("deny --all from a marked process = %v, want operator_only", err)
	}
	if left, _ := extension.ListPendingClaims(); len(left) != 2 {
		t.Fatalf("a refused decision changed the queue: %+v", left)
	}
	d.Refusal = func() string { return "" }

	// The code is the claim's own: none, another claim's and a wrong one approve nothing.
	for name, args := range map[string][]string{
		"no code":      {"approve", b.ID},
		"other's code": {"approve", b.ID, "--code", aCode},
		"wrong code":   {"approve", b.ID, "--code", "AAA-AAA"},
	} {
		if out, err := runExt(t, d, args...); err == nil {
			t.Fatalf("%s: approved (%q)", name, out)
		}
	}
	if left, _ := extension.ListPendingClaims(); len(left) != 2 {
		t.Fatalf("a wrong code changed the queue: %+v", left)
	}
	out, err := runExt(t, d, "approve", b.ID[:6], "--code", strings.ToLower(bCode))
	if err != nil || !strings.Contains(out, b.ID) || !strings.Contains(out, `"Chrome"`) || !strings.Contains(out, "instance-chrome-1") {
		t.Fatalf("approve = %q, %v", out, err)
	}
	if out, err = runExt(t, d, "deny", a.ID[:5]); err != nil || !strings.Contains(out, "Denied "+a.ID) {
		t.Fatalf("deny = %q, %v", out, err)
	}
	if _, err = runExt(t, d, "approve", "--code", aCode); err == nil {
		t.Fatal("nothing is waiting any more")
	}
}

// With exactly one waiting, no id is needed; the code still is.
func TestExtensionApproveTheOnlyWaitingRequest(t *testing.T) {
	d := pairHome(t)
	_, code := newClaim(t, "Edge", "instance-edge-1")
	if _, err := runExt(t, d, "approve"); err == nil {
		t.Fatal("approved without a code")
	}
	if out, err := runExt(t, d, "approve", "--code", code); err != nil || !strings.Contains(out, "Approved") {
		t.Fatalf("approve = %q, %v", out, err)
	}
}

// In a terminal, the code is asked for; a script has to pass --code.
func TestExtensionApprovePromptsForTheCodeInATerminal(t *testing.T) {
	d := pairHome(t)
	c, code := newClaim(t, "Edge", "instance-edge-1")
	d.StdinIsTerminal = func() bool { return true }
	cmd := NewCommand(*d)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader("wrong\n"))
	cmd.SetArgs([]string{"approve", c.ID})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatalf("a wrong typed code approved: %s", out.String())
	}
	cmd = NewCommand(*d)
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(code + "\n"))
	cmd.SetArgs([]string{"approve", c.ID})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err != nil || !strings.Contains(out.String(), "Code shown in the browser's side panel") {
		t.Fatalf("typed code = %v\n%s", err, out.String())
	}
}

// Approving says which instance it is and warns when it replaces a paired browser's token.
func TestExtensionApproveWarnsWhenItReplacesABrowser(t *testing.T) {
	d := pairHome(t)
	home, _ := os.UserHomeDir()
	seedPaired(t, home, extension.PairedBrowser{ID: "aaaa1111bbbb2222", Label: "Victim", Instance: "instance-victim-1", PairedAt: time.Now()})
	c, code := newClaim(t, "Edge", "instance-victim-1")
	out, err := runExt(t, d, "approve", c.ID, "--code", code)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"instance-victim-1", "WARNING", "aaaa1111bbbb2222", "replaces"} {
		if !strings.Contains(out, want) {
			t.Errorf("approve output lacks %q:\n%s", want, out)
		}
	}
	d2 := pairHome(t)
	c2, code2 := newClaim(t, "Edge", "instance-other-1")
	if out, _ := runExt(t, d2, "approve", c2.ID, "--code", code2); strings.Contains(out, "WARNING") {
		t.Fatalf("a warning with nothing to replace:\n%s", out)
	}
}

func TestExtensionDenyAll(t *testing.T) {
	d := pairHome(t)
	for i := 0; i < 3; i++ {
		newClaim(t, "Edge", "instance-edge-"+string(rune('a'+i)))
	}
	if _, err := runExt(t, d, "deny", "--all", "abcd"); err == nil {
		t.Fatal("--all with an id")
	}
	if _, err := runExt(t, d, "approve", "--all"); err == nil {
		t.Fatal("approve --all exists")
	}
	out, err := runExt(t, d, "deny", "--all")
	if err != nil || !strings.Contains(out, "Denied 3") {
		t.Fatalf("deny --all = %q, %v", out, err)
	}
	if left, _ := extension.ListPendingClaims(); len(left) != 0 {
		t.Fatalf("still waiting: %+v", left)
	}
}

// Terminal escapes and bidi controls in a claimant's label or origin never
// reach the owner's terminal: every output quotes them.
func TestExtensionOutputQuotesHostileText(t *testing.T) {
	d := pairHome(t)
	hostile := "Edge\x1b[2K\x1b[1A\x1b[2KMonoAgent official\u202e"
	_, _ = newClaim(t, hostile, "instance-edge-1")
	_, _ = newClaim(t, "Other", "instance-edge-2")
	var all strings.Builder
	_, err := runExt(t, d, "approve") // ambiguous: prints the waiting list in the error
	all.WriteString(err.Error())
	out, _ := runExt(t, d, "browsers", "list")
	all.WriteString(out)
	out, err = runExt(t, d, "deny", "--all")
	if err != nil {
		t.Fatalf("deny --all: %v", err)
	}
	all.WriteString(out)
	if strings.ContainsAny(all.String(), "\x1b\u202e\u2066") {
		t.Fatalf("raw control or bidi character in the output: %q", all.String())
	}
	if !strings.Contains(all.String(), "2 pairing request(s) waiting") {
		t.Fatalf("browsers list does not count the waiting requests: %q", all.String())
	}
}

func TestAutoPairCommandSaysWhereTheModeComesFrom(t *testing.T) {
	d := pairHome(t)
	out, err := runExt(t, d, "autopair")
	if err != nil || !strings.Contains(out, "Effective mode: ask (from default)") {
		t.Fatalf("unset = %q, %v", out, err)
	}
	if out, err = runExt(t, d, "autopair", "auto"); err != nil || !strings.Contains(out, "Effective mode: auto (from settings)") || !strings.Contains(out, "32 forged claims") {
		t.Fatalf("auto = %q, %v", out, err)
	}
	t.Setenv(extension.AutoPairEnv, "ask")
	if out, _ = runExt(t, d, "autopair"); !strings.Contains(out, "Effective mode: ask (from env; the stored setting is auto)") {
		t.Fatalf("env tightening = %q", out)
	}
	t.Setenv(extension.AutoPairEnv, "garbage")
	if out, _ = runExt(t, d, "autopair"); !strings.Contains(out, "Effective mode: off (from env") {
		t.Fatalf("garbage env = %q", out)
	}
	t.Setenv(extension.AutoPairEnv, "auto")
	_, _ = runExt(t, d, "autopair", "ask")
	if out, _ = runExt(t, d, "autopair"); !strings.Contains(out, "Effective mode: ask (from settings)") {
		t.Fatalf("env auto over a stored ask = %q", out)
	}
	d.JSONOutput = func() bool { return true }
	if out, _ = runExt(t, d, "autopair"); !strings.Contains(out, `"source":"settings"`) {
		t.Fatalf("json = %q", out)
	}
}
