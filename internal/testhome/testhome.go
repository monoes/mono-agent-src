// Package testhome keeps a package's tests out of the real home directory.
//
// Many code paths default to ~/.monoagent (the database, profiles, AI tool
// backups), so a test that forgets t.Setenv("HOME", …) writes into the
// user's real state — and can migrate their real database. Call Main from a
// package's TestMain to give the whole test binary a throwaway HOME:
//
//	func TestMain(m *testing.M) { testhome.Main(m) }
package testhome

import (
	"fmt"
	"github.com/monoes/mono-agent/internal/testmode"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// Main points HOME (and USERPROFILE) at a fresh temp dir, makes
// http.DefaultTransport refuse non-loopback hosts (see AllowNetworkEnv), puts failing stubs of systemctl, launchctl and
// schtasks first on PATH and fails the run if a test called one (guardServiceManagers), runs
// the tests, removes the dir and exits. Go's build and module caches, and the user cache
// dir (rod's browser download), keep their real locations so tests that
// build binaries or launch a browser don't start cold.
func Main(m *testing.M) {
	os.Exit(run(m))
}

// realHome is the home directory as it was before Main replaced it.
var realHome string

// RealHome is the user's own home directory, as it was before Main replaced
// it. Only an opt-in live test (behind a build tag) that reads the user's
// state on purpose uses it; everything else belongs in the test home.
func RealHome() string { return realHome }

// KeepHomeEnv, set to 1 by a test that starts its own test binary as a helper
// process with a HOME of its own (a rig's home, or the test's own test home),
// makes Main in that helper keep that HOME instead of replacing it. It is
// honoured only for a HOME under the temp dir that is neither the account's
// real home nor a folder holding it (keepable), so it can never point a test
// at the user's real home.
const KeepHomeEnv = "MONOAGENT_TESTHOME_KEEP"

// UnderTestEnv marks this process, and every process its tests start, as
// running under test. internal/monomind reads it (monomind.UnderTestEnv) and
// then never starts a monomind outside the temp dirs and the test home: the
// user's real install updates itself over the network and outlives the test.
const UnderTestEnv = "MONOAGENT_UNDER_TEST"

func run(m *testing.M) int {
	realHome, _ = os.UserHomeDir()
	os.Setenv(UnderTestEnv, "1")
	testmode.On = true
	// No test reaches the OS keyring: on Linux go-keyring dials the session
	// bus and, with no address set, godbus autolaunches a dbus-daemon that
	// outlives the test. A test that wants its own store (or a failing one)
	// calls MockInit / MockInitWithError again.
	keyring.MockInit()
	// No test probes or binds the user's real bridge on its default ports
	// (9222/9323): a test that wants a bridge names its own port. Port 1 is
	// never listening; the dial guard (network.go) fails the run on a dial to
	// the real ports anyway. (internal/extension.ExtensionPortEnv.)
	if os.Getenv("MONOAGENT_EXTENSION_PORT") == "" {
		os.Setenv("MONOAGENT_EXTENSION_PORT", "1")
	}
	keepCaches()
	blockNetwork()
	check, cleanup := guardServiceManagers()
	defer cleanup()
	code := runIn(m)
	if d := RealBridgeDialed(); len(d) > 0 {
		fmt.Fprintf(os.Stderr, "testhome: the tests dialed the real extension bridge's ports (they must use a private port): %v\n", d)
		if code == 0 {
			code = 1
		}
	}
	if called := check(); called != "" {
		fmt.Fprintf(os.Stderr, "testhome: the tests ran a real service manager (they must inject a fake):\n%s\n", called)
		if code == 0 {
			code = 1
		}
	}
	return code
}

func runIn(m *testing.M) int {
	if home := os.Getenv("HOME"); os.Getenv(KeepHomeEnv) == "1" && keepable(home) {
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "monoagent-testhome-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
		return 1
	}
	// TEMP/TMP point inside the home on Windows, and a directory that is the
	// working directory cannot be removed there: step out, then retry.
	defer func() {
		leaveHome(dir)
		removeHome(dir, os.RemoveAll, time.Sleep, os.Stderr)
	}()
	env, mkdirs := homeEnv(runtime.GOOS, dir)
	for _, d := range mkdirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "testhome:", err)
			return 1
		}
	}
	for _, kv := range env {
		os.Setenv(kv[0], kv[1])
	}
	return m.Run()
}

// homeEnv lists the environment (name, value pairs, in the order they are set)
// that points the account's home, config, data, state and temp locations of
// goos into dir, and the dirs that must exist for it. On Linux and macOS it
// is HOME, USERPROFILE and the XDG dirs. On Windows os.UserHomeDir,
// os.UserConfigDir and os.TempDir read USERPROFILE, APPDATA and
// TEMP/TMP, none of which follow HOME, so they are redirected too.
func homeEnv(goos, dir string) (env [][2]string, mkdirs []string) {
	if goos != "windows" {
		return [][2]string{
			{"HOME", dir},
			{"USERPROFILE", dir},
			{"XDG_CONFIG_HOME", filepath.Join(dir, ".config")},
			{"XDG_DATA_HOME", filepath.Join(dir, ".local", "share")},
			{"XDG_STATE_HOME", filepath.Join(dir, ".local", "state")},
		}, nil
	}
	join := func(elem ...string) string { return strings.Join(append([]string{dir}, elem...), `\`) }
	drive, rest := "", dir
	if len(dir) >= 2 && dir[1] == ':' {
		drive, rest = dir[:2], dir[2:]
	}
	roaming, local := join("AppData", "Roaming"), join("AppData", "Local")
	tmp := join("AppData", "Local", "Temp")
	return [][2]string{
		{"HOME", dir},
		{"USERPROFILE", dir},
		{"HOMEDRIVE", drive},
		{"HOMEPATH", rest},
		{"APPDATA", roaming},
		{"LOCALAPPDATA", local},
		{"TEMP", tmp},
		{"TMP", tmp},
		{"XDG_CONFIG_HOME", join(".config")},
		{"XDG_DATA_HOME", join(".local", "share")},
		{"XDG_STATE_HOME", join(".local", "state")},
	}, []string{roaming, local, tmp}
}

// keepCaches pins cache locations derived from HOME before HOME changes.
func keepCaches() {
	if cache, err := os.UserCacheDir(); err == nil {
		setIfUnset("GOCACHE", filepath.Join(cache, "go-build"))
		if os.Getenv("XDG_CACHE_HOME") == "" && os.Getenv("HOME") != "" {
			// Only Linux and the BSDs read it; harmless elsewhere.
			os.Setenv("XDG_CACHE_HOME", cache)
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		gopath := os.Getenv("GOPATH")
		if gopath == "" {
			gopath = filepath.Join(home, "go")
			os.Setenv("GOPATH", gopath)
		}
		setIfUnset("GOMODCACHE", filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod"))
	}
}

func setIfUnset(key, value string) {
	if os.Getenv(key) == "" {
		os.Setenv(key, value)
	}
}

// underTempDir reports whether dir lies inside a temp dir, symlinks
// resolved: os.TempDir (which follows TMPDIR), or GOTMPDIR, where t.TempDir
// makes its dirs when that is set.
func underTempDir(dir string) bool {
	if dir == "" {
		return false
	}
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	for _, root := range []string{os.TempDir(), os.Getenv("GOTMPDIR")} {
		if root == "" {
			continue
		}
		tmp, err := filepath.EvalSymlinks(root)
		if err == nil && d != tmp && within(d, tmp) {
			return true
		}
	}
	return false
}

// keepable reports whether a HOME handed to a helper process may be kept: it
// lies under the temp dir and is neither the account's real home (from the
// user database, not $HOME, which a test run may have moved) nor a folder
// that holds it. The temp dir can sit above the home as well as below it
// (TMPDIR=/ or ~/scratch/…), so "under the temp dir" alone does not rule
// those out.
func keepable(dir string) bool {
	if !underTempDir(dir) {
		return false
	}
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		return false
	}
	home, err := filepath.EvalSymlinks(u.HomeDir)
	if err != nil {
		return false
	}
	d, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	return !within(home, d)
}

// within reports whether path is dir or lies under it (both resolved).
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
