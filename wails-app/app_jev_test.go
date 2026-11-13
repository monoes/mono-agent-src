package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeJevKey = "ts_live_SECRET_do_not_leak_123"

// fakeJevCLI installs a monoagentcli stand-in that logs argv (one line per
// call) and, for `jev key set`, the stdin it received; it answers each jev
// subcommand with a canned JSON document.
func fakeJevCLI(t *testing.T) (argsLog, stdinLog string) {
	t.Helper()
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	stdinLog = filepath.Join(dir, "stdin.log")
	script := `#!/bin/sh
echo "$*" >> '` + argsLog + `'
case "$*" in
  *" jev status"*) echo '{"profile_id":"work","key_source":"vault","key_entry":"typesafe","model":"jev-latest","base_url":"https://api.typesafe.ai","surfaces":[{"surface":"hil","title":"Human-in-Loop suggestions","description":"d","egress":["a","b"],"enabled":true,"threshold":0.9,"default_threshold":0.9}]}';;
  *" jev key set"*) cat > '` + stdinLog + `'; echo '{"key_source":"vault","key_entry":"typesafe","replaced":false}';;
  *" jev key test"*) echo '{"ok":false,"key_source":"vault","models":null,"error":"401 unauthorized"}';;
  *" jev key remove"*) echo '{"removed":"typesafe"}';;
  *" jev enable "*) echo '{"profile_id":"work","surface":"hil","enabled":true,"threshold":0.8,"egress":["a"]}';;
  *" jev disable "*) echo '{"profile_id":"work","surface":"hil","enabled":false}';;
  *" jev usage"*) echo '{"profile_id":"work","since":"2026-09-18T00:00:00Z","surfaces":[{"surface":"hil","calls":3,"failures":1,"input_tokens":1200,"estimated_usd":0.00005,"avg_latency_ms":180}],"total":{"surface":"total","calls":3,"failures":1,"input_tokens":1200,"estimated_usd":0.00005,"avg_latency_ms":180}}';;
  *) echo 'unexpected' >&2; exit 2;;
esac
`
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	return argsLog, stdinLog
}

func TestJevFuncsShellOutToCLI(t *testing.T) {
	argsLog, stdinLog := fakeJevCLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")

	st, err := a.JevStatus()
	if err != nil || st.KeySource != "vault" || st.KeyEntry != "typesafe" || len(st.Surfaces) != 1 ||
		st.Surfaces[0].Title != "Human-in-Loop suggestions" || len(st.Surfaces[0].Egress) != 2 || !st.Surfaces[0].Enabled {
		t.Fatalf("JevStatus = %+v, %v", st, err)
	}
	set, err := a.JevSetKey("  " + fakeJevKey + "\n")
	if err != nil || set.KeyEntry != "typesafe" {
		t.Fatalf("JevSetKey = %+v, %v", set, err)
	}
	test, err := a.JevTestKey()
	if err != nil || test.OK || test.Error != "401 unauthorized" || test.Models == nil {
		t.Fatalf("JevTestKey = %+v, %v", test, err)
	}
	if rm, err := a.JevRemoveKey(); err != nil || rm.Removed != "typesafe" {
		t.Fatalf("JevRemoveKey = %+v, %v", rm, err)
	}
	if r, err := a.JevSetSurface("hil", true, 0.8); err != nil || !r.Enabled || r.Threshold != 0.8 {
		t.Fatalf("enable = %+v, %v", r, err)
	}
	if _, err := a.JevSetSurface("hil", true, 0); err != nil {
		t.Fatal(err)
	}
	if r, err := a.JevSetSurface("hil", false, 0.8); err != nil || r.Enabled {
		t.Fatalf("disable = %+v, %v", r, err)
	}
	u, err := a.JevUsage("")
	if err != nil || len(u.Surfaces) != 1 || u.Surfaces[0].Calls != 3 || u.Total.InputTokens != 1200 {
		t.Fatalf("JevUsage = %+v, %v", u, err)
	}

	want := []string{
		"--profile work --json jev status",
		"--profile work --json jev key set",
		"--profile work --json jev key test",
		"--profile work --json jev key remove",
		"--profile work --json jev enable hil --yes --threshold 0.8",
		"--profile work --json jev enable hil --yes",
		"--profile work --json jev disable hil",
		"--profile work --json jev usage --since 7d",
	}
	got := loggedArgs(t, argsLog)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("CLI calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The key reaches the CLI on stdin (trimmed) and never on argv.
	stdin, err := os.ReadFile(stdinLog)
	if err != nil || string(stdin) != fakeJevKey {
		t.Fatalf("stdin = %q, %v", stdin, err)
	}
	if raw, _ := os.ReadFile(argsLog); strings.Contains(string(raw), "SECRET") {
		t.Fatal("the key appeared in argv")
	}
}

func TestJevRefusesBadInputBeforeCLI(t *testing.T) {
	argsLog, _ := fakeJevCLI(t)
	a := newTestApp(t)
	a.ctx = context.Background()
	if _, err := a.JevSetKey("   "); err == nil {
		t.Fatal("empty key must be refused")
	}
	if _, err := a.JevSetSurface("", true, 0.9); err == nil {
		t.Fatal("empty surface must be refused")
	}
	if _, err := a.JevSetSurface("--yes", true, 0.9); err == nil {
		t.Fatal("flag-like surface must be refused")
	}
	if _, err := a.JevSetSurface("hil", true, 1.5); err == nil {
		t.Fatal("threshold > 1 must be refused")
	}
	if _, err := a.JevUsage("-1d"); err == nil {
		t.Fatal("flag-like --since must be refused")
	}
	if _, err := os.Stat(argsLog); !os.IsNotExist(err) {
		t.Fatalf("the CLI ran for invalid input: %v", err)
	}
}

// CLI errors (stderr) surface as the returned error.
func TestJevCLIErrorSurfaces(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, "#!/bin/sh\necho 'no TypeSafe key in the vault' >&2\nexit 1\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	a := newTestApp(t)
	a.ctx = context.Background()
	if _, err := a.JevRemoveKey(); err == nil || !strings.Contains(err.Error(), "no TypeSafe key in the vault") {
		t.Fatalf("err = %v", err)
	}
}

// TestJevRealCLI drives the real monoagentcli (opt-in: JEV_REAL_CLI=1 with
// MONOAGENTCLI_BIN set, HOME a scratch home, MONOAGENT_ALLOW_FILE_KEYRING=1
// and no TYPESAFE_API_KEY): key set → status → enable → disable → remove.
func TestJevRealCLI(t *testing.T) {
	if os.Getenv("JEV_REAL_CLI") != "1" || os.Getenv("MONOAGENTCLI_BIN") == "" {
		t.Skip("set JEV_REAL_CLI=1 and MONOAGENTCLI_BIN to run against the real CLI")
	}
	a := newTestApp(t)
	a.ctx = context.Background()

	st, err := a.JevStatus()
	if err != nil || st.KeySource != "none" || len(st.Surfaces) == 0 {
		t.Fatalf("initial status = %+v, %v", st, err)
	}
	for _, s := range st.Surfaces {
		if s.Title == "" || s.Description == "" || len(s.Egress) == 0 {
			t.Fatalf("surface %s lacks title/description/egress: %+v", s.Surface, s)
		}
	}
	if _, err := a.JevRemoveKey(); err == nil {
		t.Fatal("remove with no vault key must fail")
	}
	set, err := a.JevSetKey(fakeJevKey)
	if err != nil || set.KeySource != "vault" || set.KeyEntry != "typesafe" || set.Replaced {
		t.Fatalf("JevSetKey = %+v, %v", set, err)
	}
	if set, err = a.JevSetKey(fakeJevKey + "2"); err != nil || !set.Replaced || set.KeyEntry != "typesafe" {
		t.Fatalf("second JevSetKey = %+v, %v", set, err)
	}
	st, err = a.JevStatus()
	if err != nil || st.KeySource != "vault" || st.KeyEntry != "typesafe" {
		t.Fatalf("status after set = %+v, %v", st, err)
	}
	if r, err := a.JevSetSurface("hil", true, 0.85); err != nil || !r.Enabled || r.Threshold != 0.85 || len(r.Egress) == 0 {
		t.Fatalf("enable = %+v, %v", r, err)
	}
	st, _ = a.JevStatus()
	for _, s := range st.Surfaces {
		if s.Surface == "hil" && (!s.Enabled || s.Threshold != 0.85) {
			t.Fatalf("hil after enable = %+v", s)
		}
	}
	if r, err := a.JevSetSurface("hil", false, 0); err != nil || r.Enabled || r.Surface != "hil" {
		t.Fatalf("disable = %+v, %v", r, err)
	}
	if u, err := a.JevUsage("7d"); err != nil || u.Surfaces == nil {
		t.Fatalf("usage = %+v, %v", u, err)
	}
	if rm, err := a.JevRemoveKey(); err != nil || rm.Removed != "typesafe" {
		t.Fatalf("remove = %+v, %v", rm, err)
	}
	if st, err = a.JevStatus(); err != nil || st.KeySource != "none" {
		t.Fatalf("status after remove = %+v, %v", st, err)
	}
}
