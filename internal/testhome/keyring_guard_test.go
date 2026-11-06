package testhome

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

// A test binary started through Main gets go-keyring's in-memory store, so no
// test reaches the OS keyring (and, with no session bus address set, godbus
// never autolaunches a dbus-daemon). The bus address is pointed at a dead
// path and the display variables are dropped for the test itself, so a
// missing mock fails here instead of touching a real keyring.
func TestMainInstallsTheInMemoryKeyring(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if err := keyring.Set("monoagent-testhome-guard", "probe", "v"); err != nil {
		t.Fatalf("keyring.Set reached a real keyring instead of the in-memory mock: %v", err)
	}
	got, err := keyring.Get("monoagent-testhome-guard", "probe")
	if err != nil || got != "v" {
		t.Fatalf("keyring.Get = %q, %v; want the stored value from the in-memory mock", got, err)
	}
}

// keyringUnguarded lists the test packages (module-relative, slash-separated)
// that import go-keyring without testhome.Main or a MockInit call, and why.
// It is meant to stay empty.
var keyringUnguarded = map[string]string{}

const (
	keyringPkg  = "github.com/zalando/go-keyring"
	testhomePkg = "github.com/monoes/mono-agent/internal/testhome"
	modulePath  = "github.com/monoes/mono-agent"
)

// TestEveryTestBinaryThatReachesTheKeyringIsMocked fails for a package whose
// test binary links go-keyring (directly or through any dependency) but
// neither uses testhome (whose Main installs the mock; this test reads
// testhome.go to see that it still does) nor calls keyring.MockInit itself: its tests would talk to the OS keyring.
func TestEveryTestBinaryThatReachesTheKeyringIsMocked(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("go", "list", "-test", "-f", "{{.ImportPath}}|{{join .Deps \" \"}}", "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	src, err := os.ReadFile(filepath.Join(root, "internal", "testhome", "testhome.go"))
	if err != nil {
		t.Fatal(err)
	}
	mainMocks := bytes.Contains(src, []byte("keyring.MockInit()"))
	var bad []string
	checked := 0
	for _, line := range strings.Split(string(out), "\n") {
		path, deps, ok := strings.Cut(line, "|")
		if !ok || !strings.HasSuffix(path, ".test") {
			continue
		}
		checked++
		set := " " + deps + " "
		if !strings.Contains(set, " "+keyringPkg+" ") {
			continue
		}
		if strings.Contains(set, " "+testhomePkg+" ") && mainMocks {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimSuffix(path, ".test"), modulePath+"/")
		if strings.TrimSuffix(path, ".test") == modulePath {
			rel = "."
		}
		if _, ok := keyringUnguarded[rel]; ok || callsMockInit(t, filepath.Join(root, filepath.FromSlash(rel))) {
			continue
		}
		bad = append(bad, rel)
	}
	if checked < 100 {
		t.Fatalf("go list returned only %d test binaries: is the listing broken?", checked)
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("%d test package(s) link go-keyring but use neither testhome.Main nor keyring.MockInit, so their tests can reach the OS keyring "+
			"(and start a dbus-daemon):\n  %s", len(bad), strings.Join(bad, "\n  "))
	}
}

func callsMockInit(t *testing.T, dir string) bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(src, []byte("keyring.MockInit")) {
			return true
		}
	}
	return false
}
