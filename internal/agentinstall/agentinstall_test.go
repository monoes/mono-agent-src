package agentinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/nodemgr"
)

func TestParse(t *testing.T) {
	cases := []struct {
		hint string
		want Recipe
	}{
		{"npm install -g @anthropic-ai/claude-code", Recipe{Kind: KindNpm, Packages: []string{"@anthropic-ai/claude-code"}}},
		{"npm install -g opencode-ai", Recipe{Kind: KindNpm, Packages: []string{"opencode-ai"}}},
		{"npm install --global a b@1.2", Recipe{Kind: KindNpm, Packages: []string{"a", "b@1.2"}}},
		{"npm install ai (plus the vendor model package)", Recipe{Kind: KindManual}},
		{"npm install -g foo; echo injected", Recipe{Kind: KindManual}},
		{"npm install -g --unsafe-perm foo", Recipe{Kind: KindManual}},
		{"install the Grok Build CLI per https://docs.x.ai/build/cli", Recipe{Kind: KindManual}},
		{"curl -fsSL http://example.com/install.sh | bash", Recipe{Kind: KindManual}},
		{"curl -fsSL https://example.com/i.sh | bash; echo injected", Recipe{Kind: KindManual}},
	}
	if runtime.GOOS != "windows" {
		cases = append(cases, struct {
			hint string
			want Recipe
		}{"curl -fsSL https://antigravity.google/cli/install.sh | bash",
			Recipe{Kind: KindScript, ScriptURL: "https://antigravity.google/cli/install.sh", Shell: "bash"}})
	}
	for _, tc := range cases {
		got := Parse(tc.hint)
		tc.want.Hint = tc.hint
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Parse(%q) = %+v, want %+v", tc.hint, got, tc.want)
		}
	}
}

func TestInstallManualIsRefused(t *testing.T) {
	err := New().Install(context.Background(), Parse("install it by hand"), func(string) {})
	if !errors.Is(err, ErrManual) {
		t.Fatalf("got %v, want ErrManual", err)
	}
}

func TestInstallScriptRunsDownloadedInstaller(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	marker := filepath.Join(t.TempDir(), "ran")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("echo installing\ntouch " + marker + "\n"))
	}))
	defer srv.Close()

	in := New()
	in.HTTP = srv.Client()
	u, _ := url.Parse(srv.URL)
	ScriptHosts[u.Host] = true // the test server's host:port
	t.Cleanup(func() { delete(ScriptHosts, u.Host) })
	var lines []string
	r := Recipe{Kind: KindScript, ScriptURL: srv.URL + "/install.sh", Shell: "sh"}
	if err := in.Install(context.Background(), r, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("installer did not run")
	}
	log := strings.Join(lines, "\n")
	if !strings.Contains(log, "sha256") || !strings.Contains(log, "installing") {
		t.Errorf("log should show the installer's hash and output:\n%s", log)
	}
}

// serveInstaller serves body over TLS as an allowed installer host.
func serveInstaller(t *testing.T, body string) (*Installer, string) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	t.Cleanup(srv.Close)
	in := New()
	in.HTTP = srv.Client()
	u, _ := url.Parse(srv.URL)
	ScriptHosts[u.Host] = true
	t.Cleanup(func() { delete(ScriptHosts, u.Host) })
	return in, srv.URL + "/install.sh"
}

// The recipe's sha256 is what was reviewed: a script that hashes differently
// is not run (r08-D03).
func TestInstallScriptRefusesAHashMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	marker := filepath.Join(t.TempDir(), "ran")
	body := "touch " + marker + "\n"
	in, u := serveInstaller(t, body)
	r := Recipe{Kind: KindScript, ScriptURL: u, Shell: "sh", ScriptSHA256: strings.Repeat("ab", 32)}
	err := in.Install(context.Background(), r, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("mismatch: err = %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the mismatching installer ran")
	}
	sum := sha256.Sum256([]byte(body))
	r.ScriptSHA256 = strings.ToUpper(hex.EncodeToString(sum[:]))
	if err := in.Install(context.Background(), r, func(string) {}); err != nil {
		t.Fatalf("matching hash: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the matching installer did not run")
	}
}

// The installer script sees a scrubbed environment, not the caller's tokens (r08-D03).
func TestInstallScriptEnvIsScrubbed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	t.Setenv("MONOAGENT_API_TOKEN", "tok-secret")
	t.Setenv("GITHUB_TOKEN", "gh-secret")
	out := filepath.Join(t.TempDir(), "env")
	in, u := serveInstaller(t, "env > "+out+"\n")
	if err := in.Install(context.Background(), Recipe{Kind: KindScript, ScriptURL: u, Shell: "sh"}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if strings.Contains(string(b), "secret") || !strings.Contains(string(b), "PATH=") {
		t.Errorf("installer env:\n%s", b)
	}
}

func TestForEntryCarriesSha256AndIgnoreScripts(t *testing.T) {
	e := monomind.ScanEntry{Install: &monomind.InstallRecipe{Kind: "npm", Packages: []string{"@openai/codex@1.2.3"}, IgnoreScripts: true}}
	if r := ForEntry(e); !r.IgnoreScripts || r.Kind != KindNpm {
		t.Errorf("npm: %+v", r)
	}
	if runtime.GOOS == "windows" {
		return
	}
	e = monomind.ScanEntry{Install: &monomind.InstallRecipe{Kind: "script", URL: "https://antigravity.google/install.sh", Shell: "bash", SHA256: "AB"}}
	if r := ForEntry(e); r.ScriptSHA256 != "AB" {
		t.Errorf("script: %+v", r)
	}
}

func TestForEntryPrefersAndRevalidatesMonomindRecipe(t *testing.T) {
	npm := monomind.ScanEntry{InstallHint: "anything", Install: &monomind.InstallRecipe{Kind: "npm", Packages: []string{"@openai/codex"}}}
	if r := ForEntry(npm); r.Kind != KindNpm || r.Packages[0] != "@openai/codex" {
		t.Errorf("npm recipe: %+v", r)
	}
	bad := monomind.ScanEntry{Install: &monomind.InstallRecipe{Kind: "npm", Packages: []string{"x; echo injected"}}}
	if r := ForEntry(bad); r.Kind != KindManual {
		t.Errorf("unsafe package accepted: %+v", r)
	}
	http := monomind.ScanEntry{Install: &monomind.InstallRecipe{Kind: "script", URL: "http://x/i.sh", Shell: "bash"}}
	if r := ForEntry(http); r.Kind != KindManual {
		t.Errorf("http script accepted: %+v", r)
	}
	old := monomind.ScanEntry{InstallHint: "npm install -g opencode-ai"} // monomind before rev 9
	if r := ForEntry(old); r.Kind != KindNpm {
		t.Errorf("fallback to hint: %+v", r)
	}
}

// A spec npm reads as a tarball path is not a registry package.
func TestTarballSpecsAreManual(t *testing.T) {
	for _, hint := range []string{"npm install -g foo.tgz", "npm install -g @x/y.tar.gz", "npm install -g foo.TAR"} {
		if r := Parse(hint); r.Kind != KindManual {
			t.Errorf("Parse(%q) = %+v, want manual", hint, r)
		}
	}
	e := monomind.ScanEntry{Install: &monomind.InstallRecipe{Kind: "npm", Packages: []string{"evil.tgz"}}}
	if r := ForEntry(e); r.Kind != KindManual {
		t.Errorf("ForEntry accepted a tarball: %+v", r)
	}
	err := New().Install(context.Background(), Recipe{Kind: KindNpm, Packages: []string{"evil.tgz"}}, func(string) {})
	if !errors.Is(err, ErrManual) {
		t.Errorf("Install ran a hand-built tarball recipe: %v", err)
	}
}

// Vendor scripts run only from the hosts in ScriptHosts; any other https
// URL is a manual step, whether it came as a hint or a structured recipe.
func TestScriptHostAllowlist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no scripts on Windows")
	}
	for _, u := range []string{
		"https://antigravity.google/cli/install.sh",
		"https://hermes-agent.nousresearch.com/install.sh",
	} {
		if r := Parse("curl -fsSL " + u + " | bash"); r.Kind != KindScript {
			t.Errorf("%s: %+v, want script", u, r)
		}
	}
	for _, u := range []string{
		"https://example.com/install.sh",
		"https://antigravity.google.evil.com/i.sh",
		"https://antigravity.google:8443/i.sh",
		"https://user@antigravity.google/i.sh",
	} {
		if r := Parse("curl -fsSL " + u + " | bash"); r.Kind != KindManual {
			t.Errorf("%s: %+v, want manual", u, r)
		}
		e := monomind.ScanEntry{Install: &monomind.InstallRecipe{Kind: "script", URL: u, Shell: "bash"}}
		if r := ForEntry(e); r.Kind != KindManual {
			t.Errorf("ForEntry(%s): %+v, want manual", u, r)
		}
	}
	err := New().Install(context.Background(), Recipe{Kind: KindScript, ScriptURL: "https://example.com/i.sh", Shell: "sh"}, func(string) {})
	if !errors.Is(err, ErrManual) {
		t.Errorf("Install ran a script from a host not on the list: %v", err)
	}
}

// fakeNpmMachine puts a suitable `node` and an `npm` on PATH. The npm
// answers `prefix -g` with sysPrefix and, for `install -g <pkg>`, prints a
// line and creates <prefix>/bin/<pkg> in NPM_CONFIG_PREFIX when set (the
// private-prefix case) or sysPrefix; `install -g fail` exits 1.
func fakeNpmMachine(t *testing.T, sysPrefix string) *nodemgr.Manager {
	t.Helper()
	home := t.TempDir()
	sys := t.TempDir()
	os.WriteFile(filepath.Join(sys, "node"), []byte("#!/bin/sh\necho v24.5.0\n"), 0o755)
	npm := `#!/bin/sh
if [ "$1" = prefix ]; then echo "` + sysPrefix + `"; exit 0; fi
p="${NPM_CONFIG_PREFIX:-` + sysPrefix + `}"
shift 2
for pkg in "$@"; do
  if [ "$pkg" = fail ]; then echo "npm error EEXIST: file already exists" >&2; exit 1; fi
  echo "added 1 package: $pkg"
  mkdir -p "$p/bin" && printf '#!/bin/sh\n' > "$p/bin/$pkg" && chmod 755 "$p/bin/$pkg"
done
`
	os.WriteFile(filepath.Join(sys, "npm"), []byte(npm), 0o755)
	t.Setenv("PATH", sys+string(os.PathListSeparator)+"/bin:/usr/bin")
	t.Setenv("NPM_CONFIG_PREFIX", "")
	return &nodemgr.Manager{Root: filepath.Join(home, "node"), NpmRoot: filepath.Join(home, "npm-global"),
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
}

// npm recipes go through nodemgr.InstallGlobal and StreamCmd: output is
// streamed line by line, the executables land in the planned bin folder,
// that folder is put on PATH, and a failure carries npm's last lines.
func TestInstallNpmStreamsAndPlacesBinaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script npm")
	}
	prefix := t.TempDir()
	in := &Installer{Node: fakeNpmMachine(t, prefix)}
	ctx := context.Background()
	if dir, err := in.NpmBinDir(ctx); err != nil || dir != filepath.Join(prefix, "bin") {
		t.Fatalf("NpmBinDir = %q, %v", dir, err)
	}
	var lines []string
	if err := in.Install(ctx, Recipe{Kind: KindNpm, Packages: []string{"tool"}}, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "added 1 package: tool") {
		t.Errorf("npm output not streamed: %q", lines)
	}
	if _, err := os.Stat(filepath.Join(prefix, "bin", "tool")); err != nil {
		t.Errorf("executable not in the system prefix: %v", err)
	}
	if !strings.Contains(os.Getenv("PATH"), filepath.Join(prefix, "bin")) {
		t.Errorf("bin folder not put on PATH: %s", os.Getenv("PATH"))
	}

	err := in.Install(ctx, Recipe{Kind: KindNpm, Packages: []string{"fail"}}, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "EEXIST") {
		t.Errorf("failure should carry npm's last lines: %v", err)
	}
}

// A system prefix npm can't write to (root-owned /usr/local) is never
// used with sudo: the install goes to the private npm-global instead.
// A read-only temp folder stands in for it on every OS.
func TestInstallNpmUnwritablePrefixUsesPrivateOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script npm")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	prefix := t.TempDir()
	os.MkdirAll(filepath.Join(prefix, "bin"), 0o755)
	os.MkdirAll(filepath.Join(prefix, "lib", "node_modules"), 0o755)
	for _, d := range []string{filepath.Join(prefix, "bin"), filepath.Join(prefix, "lib", "node_modules")} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(d, 0o755) })
	}
	m := fakeNpmMachine(t, prefix)
	in := &Installer{Node: m}
	if dir, _ := in.NpmBinDir(context.Background()); dir != m.NpmBinDir() {
		t.Fatalf("NpmBinDir = %q, want the private %q", dir, m.NpmBinDir())
	}
	if err := in.Install(context.Background(), Recipe{Kind: KindNpm, Packages: []string{"tool"}}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.NpmBinDir(), "tool")); err != nil {
		t.Errorf("not installed into the private prefix: %v", err)
	}
}
