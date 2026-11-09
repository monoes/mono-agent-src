package nodemgr

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
)

type tarEntry struct {
	name, body, link string
	dir              bool
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o755}
		switch {
		case e.dir:
			hdr.Typeflag = tar.TypeDir
		case e.link != "":
			hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, e.link
		default:
			hdr.Typeflag, hdr.Size = tar.TypeReg, int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// fakeDist serves index.json, SHASUMS256.txt.asc (signed by a test key m
// is told to trust) and one archive per version.
func fakeDist(t *testing.T, m *Manager, archives map[string][]byte, corrupt bool) *httptest.Server {
	t.Helper()
	signer := testSigner(t)
	m.keys = openpgp.EntityList{signer}
	mux := http.NewServeMux()
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		key := m.indexFileKey()
		fmt.Fprintf(w, `[
 {"version":"v26.1.0","lts":false,"files":[%q]},
 {"version":"v24.2.0","lts":"Krypton","files":["some-other-platform"]},
 {"version":"v24.1.0","lts":"Krypton","files":[%q]},
 {"version":"v22.11.0","lts":"Jod","files":[%q]}
]`, key, key, key)
	})
	for v, data := range archives {
		name := m.archiveName(v)
		sum := sha256.Sum256(data)
		if corrupt {
			sum[0] ^= 0xff
		}
		data := data
		asc := clearsignText(t, signer, fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name))
		mux.HandleFunc("/v"+v+"/SHASUMS256.txt.asc", func(w http.ResponseWriter, r *http.Request) { w.Write(asc) })
		mux.HandleFunc("/v"+v+"/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(data) })
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testManager(t *testing.T) *Manager {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell-script stand-in for node")
	}
	m := New()
	dir := t.TempDir()
	m.Root, m.NpmRoot = filepath.Join(dir, "node"), filepath.Join(dir, "npm-global")
	return m
}

func nodeArchive(t *testing.T, m *Manager, version string) []byte {
	top := strings.TrimSuffix(m.archiveName(version), ".tar.gz") + "/"
	return makeTarGz(t, []tarEntry{
		{name: top, dir: true},
		{name: top + "bin/node", body: "#!/bin/sh\necho v" + version + "\n"},
		{name: top + "lib/node_modules/npm/bin/npm-cli.js", body: "// npm"},
		{name: top + "bin/npm", link: "../lib/node_modules/npm/bin/npm-cli.js"},
	})
}

func TestResolveVersion(t *testing.T) {
	m := testManager(t)
	m.BaseURL = fakeDist(t, m, nil, false).URL
	ctx := context.Background()
	for want, exp := range map[string]string{"": "24.1.0", "lts": "24.1.0", "26": "26.1.0", "v24.9.9": "24.9.9"} {
		got, err := m.ResolveVersion(ctx, want)
		if err != nil || got != exp {
			t.Errorf("ResolveVersion(%q) = %q, %v; want %q", want, got, err, exp)
		}
	}
	if _, err := m.ResolveVersion(ctx, "22"); err == nil {
		t.Error("22.x only has releases below MinVersion; want an error")
	}
}

func TestInstallUseRemove(t *testing.T) {
	m := testManager(t)
	m.BaseURL = fakeDist(t, m, map[string][]byte{"24.1.0": nodeArchive(t, m, "24.1.0")}, false).URL
	ctx := context.Background()

	var lines []string
	v, err := m.Install(ctx, "lts", func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatalf("Install: %v\n%s", err, strings.Join(lines, "\n"))
	}
	if cur, ok := m.Current(); v != "24.1.0" || !ok || cur != v {
		t.Fatalf("installed %q, current %q/%v", v, cur, ok)
	}
	if got, _ := NodeVersion(ctx, m.NodePath(v)); got != "24.1.0" {
		t.Fatalf("node --version = %q", got)
	}
	if link, err := os.Readlink(m.NpmPath(v)); err != nil || link != "../lib/node_modules/npm/bin/npm-cli.js" {
		t.Fatalf("npm symlink: %q, %v", link, err)
	}
	if inst, _ := m.Installed(); len(inst) != 1 || inst[0] != v {
		t.Fatalf("Installed = %v", inst)
	}
	// Staging and download temp files are gone.
	entries, _ := os.ReadDir(m.Root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && e.Name() != lockFile && e.Name() != sysCacheFile {
			t.Errorf("leftover %s", e.Name())
		}
	}

	// Installing again only re-activates.
	if _, err := m.Install(ctx, "24.1.0", nil); err != nil {
		t.Fatal(err)
	}

	env := strings.Join(m.NpmEnv(v), "\n")
	if !strings.Contains(env, "NPM_CONFIG_PREFIX="+m.NpmRoot) || !strings.Contains(env, "PATH="+m.BinDir(v)) {
		t.Errorf("NpmEnv missing prefix/PATH:\n%s", env)
	}

	if err := m.Remove(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Current(); ok {
		t.Error("still current after Remove")
	}
}

func TestInstallRejectsBadChecksum(t *testing.T) {
	m := testManager(t)
	m.BaseURL = fakeDist(t, m, map[string][]byte{"24.1.0": nodeArchive(t, m, "24.1.0")}, true).URL
	_, err := m.Install(context.Background(), "24.1.0", nil)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	if inst, _ := m.Installed(); len(inst) != 0 {
		t.Fatalf("nothing should be installed: %v", inst)
	}
}

func TestInstallRejectsEscapingArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar symlinks are a unix install path")
	}
	for _, entries := range [][]tarEntry{
		{{name: "../evil", body: "x"}},
		{{name: "node-v24.1.0/bin/npm", link: "../../../../etc/passwd"}},
		{{name: "node-v24.1.0/bin/npm", link: "/etc/passwd"}},
		// Each link is inside the folder as text, but they chain: x/y is
		// the folder itself, so x/y/z -> .. is its parent, and x/y/z/pwned
		// would land outside.
		{{name: "x/", dir: true}, {name: "x/y", link: ".."}, {name: "x/y/z", link: ".."}, {name: "x/y/z/pwned", body: "x"}},
	} {
		arch := makeTarGz(t, entries)
		path := filepath.Join(t.TempDir(), "a.tar.gz")
		os.WriteFile(path, arch, 0o644)
		// The staging folder sits inside a parent the test watches, so a
		// write that escapes it is seen.
		parent := t.TempDir()
		staging := filepath.Join(parent, "staging")
		if err := os.Mkdir(staging, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := untarGz(path, staging, 1<<20); err == nil {
			t.Errorf("entries %+v: want rejection", entries)
		}
		for _, escaped := range []string{filepath.Join(parent, "pwned"), filepath.Join(parent, "evil")} {
			if _, err := os.Lstat(escaped); err == nil {
				t.Errorf("entries %+v: wrote %s outside the staging folder", entries, escaped)
			}
		}
	}
}

// remove takes only a version: anything else used to name a folder above
// the managed Node (`remove ..` deleted all of ~/.monoagent).
func TestRemoveAndUseTakeOnlyAVersion(t *testing.T) {
	m := testManager(t)
	sentinel := filepath.Join(filepath.Dir(m.Root), "monoagent.db")
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"..", "../..", "../node", ".", "v..", "24", "24.1", "24.1.0/..", "/tmp", "24.1.0.1", "-1.0.0"} {
		if err := m.Remove(bad); err == nil {
			t.Errorf("Remove(%q) succeeded", bad)
		}
		if err := m.Use(bad); err == nil {
			t.Errorf("Use(%q) succeeded", bad)
		}
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("a file next to the managed Node was deleted: %v", err)
	}
	if err := m.Remove("24.9.9"); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("Remove of a version not installed = %v, want an error", err)
	}
}

func TestCompareAndSuitable(t *testing.T) {
	if Compare("v22.12.0", "22.9.0") != 1 || Compare("22.12.0", "22.12.0") != 0 || Compare("junk", "1.0.0") != -1 {
		t.Error("Compare")
	}
	if Suitable("22.11.9") || !Suitable("v22.12.0") || !Suitable("26.0.0") {
		t.Error("Suitable")
	}
}

func TestActivatePrefersSuitableSystemNode(t *testing.T) {
	m := testManager(t)
	m.BaseURL = fakeDist(t, m, map[string][]byte{"24.1.0": nodeArchive(t, m, "24.1.0")}, false).URL
	home := t.TempDir()
	testhome.SetHome(t, home)
	m.Root, m.NpmRoot = filepath.Join(home, ".monoagent", "node"), filepath.Join(home, ".monoagent", "npm-global")
	if _, err := m.Install(context.Background(), "24.1.0", nil); err != nil {
		t.Fatal(err)
	}

	// Old system node: managed goes first.
	sys := t.TempDir()
	os.WriteFile(filepath.Join(sys, "node"), []byte("#!/bin/sh\necho v20.0.0\n"), 0o755)
	t.Setenv("PATH", sys+string(os.PathListSeparator)+"/bin:/usr/bin")
	t.Setenv("NPM_CONFIG_PREFIX", "")
	t.Setenv("npm_config_prefix", "")
	Activate(context.Background())
	if first := filepath.SplitList(os.Getenv("PATH"))[0]; first != m.BinDir("24.1.0") {
		t.Fatalf("old system node: PATH starts with %s", first)
	}
	// npm installs globally under npm-global, not inside the version folder
	// that `nodejs update` prunes.
	if got := os.Getenv("NPM_CONFIG_PREFIX"); got != m.NpmRoot {
		t.Fatalf("NPM_CONFIG_PREFIX = %q, want %q", got, m.NpmRoot)
	}
	// A prefix the user set is kept.
	t.Setenv("NPM_CONFIG_PREFIX", "/home/me/.npm-global")
	Activate(context.Background())
	if got := os.Getenv("NPM_CONFIG_PREFIX"); got != "/home/me/.npm-global" {
		t.Fatalf("the user's own prefix was replaced with %q", got)
	}

	// Suitable system node: it stays first, managed is appended.
	os.WriteFile(filepath.Join(sys, "node"), []byte("#!/bin/sh\necho v24.5.0\n"), 0o755)
	t.Setenv("PATH", sys+string(os.PathListSeparator)+"/bin:/usr/bin")
	Activate(context.Background())
	parts := filepath.SplitList(os.Getenv("PATH"))
	if parts[0] != sys || parts[len(parts)-2] != m.BinDir("24.1.0") {
		t.Fatalf("suitable system node: PATH = %v", parts)
	}
}

func TestPlanGlobalInstall(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()

	// No node at all.
	t.Setenv("PATH", t.TempDir())
	if _, err := m.PlanGlobalInstall(ctx); err == nil {
		t.Fatal("want an error without any Node")
	}

	// Suitable system node whose global prefix is writable: use it as-is.
	sys := t.TempDir()
	prefix := t.TempDir()
	os.WriteFile(filepath.Join(sys, "node"), []byte("#!/bin/sh\necho v24.5.0\n"), 0o755)
	os.WriteFile(filepath.Join(sys, "npm"), []byte("#!/bin/sh\necho "+prefix+"\n"), 0o755)
	t.Setenv("PATH", sys)
	plan, err := m.PlanGlobalInstall(ctx)
	if err != nil || plan.Prefix != prefix || plan.Managed {
		t.Fatalf("writable system prefix: %+v, %v", plan, err)
	}

	// Unwritable prefix: fall back to the private one, never sudo. A
	// read-only folder (not /proc, which macOS doesn't have).
	if os.Geteuid() == 0 {
		t.Skip("root can write a read-only folder")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	os.MkdirAll(locked, 0o755)
	os.Chmod(locked, 0o555)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	os.WriteFile(filepath.Join(sys, "npm"), []byte("#!/bin/sh\necho "+locked+"\n"), 0o755)
	plan, err = m.PlanGlobalInstall(ctx)
	if err != nil || plan.Prefix != m.NpmRoot || !strings.Contains(strings.Join(plan.Env, "\n"), "NPM_CONFIG_PREFIX="+m.NpmRoot) {
		t.Fatalf("unwritable system prefix: %+v, %v", plan, err)
	}

	// Only a managed node: its npm with the private prefix.
	m.BaseURL = fakeDist(t, m, map[string][]byte{"24.1.0": nodeArchive(t, m, "24.1.0")}, false).URL
	if _, err := m.Install(ctx, "24.1.0", nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	plan, err = m.PlanGlobalInstall(ctx)
	if err != nil || !plan.Managed || plan.Npm != m.NpmPath("24.1.0") {
		t.Fatalf("managed only: %+v, %v", plan, err)
	}
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

// TestWindowsInstallFlow runs the Windows install path (zip, node.exe at the
// top, npm.cmd, no bin/) on this machine with the version probe stubbed.
func TestWindowsInstallFlow(t *testing.T) {
	m := New()
	dir := t.TempDir()
	m.Root, m.NpmRoot = filepath.Join(dir, "node"), filepath.Join(dir, "npm-global")
	m.GOOS, m.GOARCH = "windows", "amd64"
	var probed string
	m.probe = func(_ context.Context, node string) (string, error) { probed = node; return "24.1.0", nil }

	if got := m.archiveName("24.1.0"); got != "node-v24.1.0-win-x64.zip" {
		t.Fatalf("archive %q", got)
	}
	if got := m.indexFileKey(); got != "win-x64-zip" {
		t.Fatalf("index key %q", got)
	}
	top := "node-v24.1.0-win-x64/"
	m.BaseURL = fakeDist(t, m, map[string][]byte{"24.1.0": makeZip(t, map[string]string{
		top + "node.exe": "MZ", top + "npm.cmd": "@echo npm", top + "node_modules/npm/package.json": "{}",
	})}, false).URL

	v, err := m.Install(context.Background(), "lts", nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	root := filepath.Join(m.Root, "24.1.0")
	if m.BinDir(v) != root || m.NodePath(v) != filepath.Join(root, "node.exe") || m.NpmPath(v) != filepath.Join(root, "npm.cmd") {
		t.Fatalf("windows layout: bin %s node %s npm %s", m.BinDir(v), m.NodePath(v), m.NpmPath(v))
	}
	if probed != m.NodePath(v) {
		t.Fatalf("probed %q", probed)
	}
	for _, f := range []string{m.NodePath(v), m.NpmPath(v)} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("missing %s", f)
		}
	}
	if m.NpmBinDir() != m.NpmRoot {
		t.Errorf("windows npm bin dir %s", m.NpmBinDir())
	}

	// A zip entry escaping the target is refused.
	bad := filepath.Join(t.TempDir(), "bad.zip")
	os.WriteFile(bad, makeZip(t, map[string]string{"../evil.txt": "x"}), 0o644)
	if err := unzip(bad, t.TempDir(), 1<<20); err == nil {
		t.Error("escaping zip entry accepted")
	}
}

func TestDarwinArchiveNames(t *testing.T) {
	m := New()
	m.GOOS, m.GOARCH = "darwin", "arm64"
	if got := m.archiveName("24.1.0"); got != "node-v24.1.0-darwin-arm64.tar.gz" {
		t.Errorf("archive %q", got)
	}
	if got := m.indexFileKey(); got != "osx-arm64-tar" {
		t.Errorf("index key %q", got)
	}
	m.GOARCH = "amd64"
	if got := m.indexFileKey(); got != "osx-x64-tar" {
		t.Errorf("index key %q", got)
	}
}
