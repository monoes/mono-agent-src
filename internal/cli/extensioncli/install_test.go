package extensioncli

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// fixtureExtensionZip builds a zip shaped like the release asset: every file
// under one chrome-extension/ folder.
func fixtureExtensionZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func runExtensionInstall(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newExtensionInstallCmd(testDeps(false))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// A nil slice would make cobra read os.Args.
	cmd.SetArgs(append([]string{}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestExtensionInstallReplacesThePreviousCopy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".monoagent", "extension")
	first := fixtureExtensionZip(t, map[string]string{
		"chrome-extension/manifest.json": "v1",
		"chrome-extension/removed.js":    "old",
	})
	if err := installExtensionZip(first, dir); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(t.TempDir(), "ext.zip")
	if err := os.WriteFile(second, fixtureExtensionZip(t, map[string]string{"chrome-extension/manifest.json": "v2"}), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runExtensionInstall(t, "--zip", second)
	if err != nil {
		t.Fatalf("install --zip: %v\n%s", err, out)
	}
	if !strings.Contains(out, "not the extension this CLI ships") {
		t.Errorf("a local zip must say it was not checked:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "manifest.json")); string(b) != "v2" {
		t.Errorf("manifest.json = %q, want v2", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "removed.js")); !os.IsNotExist(err) {
		t.Errorf("a file the new zip lacks survived the reinstall")
	}
	entries, _ := os.ReadDir(filepath.Dir(dir))
	if len(entries) != 1 {
		t.Errorf("leftover staging folders next to the extension: %v", entries)
	}
}

func TestExtensionInstallKeepsThePreviousCopyOnABadZip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "extension")
	if err := installExtensionZip(fixtureExtensionZip(t, map[string]string{"chrome-extension/manifest.json": "v1"}), dir); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"zip slip":    fixtureExtensionZip(t, map[string]string{"../evil.js": "x", "manifest.json": "{}"}),
		"no manifest": fixtureExtensionZip(t, map[string]string{"chrome-extension/background.js": "x"}),
		"not a zip":   []byte("not a zip"),
	}
	for name, data := range cases {
		if err := installExtensionZip(data, dir); err == nil {
			t.Errorf("%s: installed", name)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "manifest.json")); string(b) != "v1" {
			t.Errorf("%s: the previous extension was not kept (manifest.json = %q)", name, b)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.js")); !os.IsNotExist(err) {
		t.Errorf("zip slip wrote outside the extension folder")
	}
}

func TestExtensionInstallIsRegistered(t *testing.T) {
	for _, c := range newExtensionCmd(testDeps(false)).Commands() {
		if c.Name() == "install" {
			return
		}
	}
	t.Fatal("`extension install` is not registered")
}

func TestExtensionInstallUnpacksTheBundledExtension(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	bundledExtension = fstest.MapFS{
		"manifest.json": {Data: []byte(`{"version":"9.9.9"}`)},
		"background.js": {Data: []byte("bg")},
		"x.test.mjs":    {Data: []byte("test only")},
		"sub/nested.js": {Data: []byte("n")},
	}
	out, err := runExtensionInstall(t)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	dir := filepath.Join(home, ".monoagent", "extension")
	for _, want := range []string{"manifest.json", "background.js", "sub/nested.js"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("%s was not unpacked: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x.test.mjs")); !os.IsNotExist(err) {
		t.Errorf("a unit test file was unpacked")
	}
	for _, want := range []string{"chrome://extensions", "Developer mode", "Load unpacked and choose " + dir} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestExtensionInfoReportsMissingStaleAndCurrent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	setBundledExtension(t, fstest.MapFS{"manifest.json": {Data: []byte(`{"version":"2.0.0"}`)}})
	info := func() extensionInfo {
		t.Helper()
		var out bytes.Buffer
		cmd := newExtensionInfoCmd(testDeps(true))
		cmd.SetOut(&out)
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var i extensionInfo
		if err := json.Unmarshal(out.Bytes(), &i); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out.String())
		}
		return i
	}
	dir := filepath.Join(home, ".monoagent", "extension")
	if i := info(); i.Installed || i.Path != dir {
		t.Errorf("before install: %+v", i)
	}
	if err := installExtensionFS(fstest.MapFS{"manifest.json": {Data: []byte(`{"version":"1.0.0"}`)}}, dir); err != nil {
		t.Fatal(err)
	}
	if i := info(); !i.Installed || i.Current || i.Version != "1.0.0" {
		t.Errorf("an older copy must be installed but not current: %+v", i)
	}
	if _, err := runExtensionInstall(t); err != nil {
		t.Fatal(err)
	}
	if i := info(); !i.Current || i.Version != "2.0.0" {
		t.Errorf("after install: %+v", i)
	}
}

func setBundledExtension(t *testing.T, f fs.FS) {
	t.Helper()
	old := bundledExtension
	bundledExtension = f
	t.Cleanup(func() { bundledExtension = old })
}

func TestTheBundledExtensionIsTheRealOne(t *testing.T) {
	if got := manifestVersion(bundledExtension); got == "" {
		t.Fatal("the extension built into the CLI has no manifest.json version")
	}
}
