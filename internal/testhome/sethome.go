package testhome

import (
	"runtime"
	"strings"
	"testing"
)

// SetHome points the account's home at dir for one test: the replacement for
// t.Setenv("HOME", dir). On Windows os.UserHomeDir, os.UserConfigDir and the
// profile paths read USERPROFILE, APPDATA and LOCALAPPDATA, none of which
// follow HOME, so a test that sets HOME alone still works on the account's
// real profile there. TestNoTestSetsHomeAlone fails for any test that
// goes back to setting HOME by itself.
func SetHome(t testing.TB, dir string) {
	t.Helper()
	for _, kv := range homeOnlyEnv(runtime.GOOS, dir) {
		t.Setenv(kv[0], kv[1])
	}
}

// homeOnlyEnv is the environment SetHome sets for goos (name, value pairs).
// The temp dir and the XDG dirs are left alone: tests keep t.TempDir() where
// it is, and a test that wants other dirs sets them itself.
func homeOnlyEnv(goos, dir string) [][2]string {
	env := [][2]string{{"HOME", dir}, {"USERPROFILE", dir}}
	if goos != "windows" {
		return env
	}
	if dir == "" { // a test of "no home at all": nothing may resolve against a drive root
		return append(env, [2]string{"HOMEDRIVE", ""}, [2]string{"HOMEPATH", ""}, [2]string{"APPDATA", ""}, [2]string{"LOCALAPPDATA", ""})
	}
	drive, rest := "", dir
	if len(dir) >= 2 && dir[1] == ':' {
		drive, rest = dir[:2], dir[2:]
	}
	join := func(elem ...string) string { return strings.Join(append([]string{dir}, elem...), `\`) }
	return append(env,
		[2]string{"HOMEDRIVE", drive},
		[2]string{"HOMEPATH", rest},
		[2]string{"APPDATA", join("AppData", "Roaming")},
		[2]string{"LOCALAPPDATA", join("AppData", "Local")},
	)
}
