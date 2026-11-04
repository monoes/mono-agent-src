package testhome

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// serviceManagers are the programs that start, stop and restart the user's real services (the daemon, the extension
// bridge): systemd, launchd and the Windows task scheduler. A test never talks to them for real: its service manager is a fake
// it injects.
var serviceManagers = []string{"systemctl", "launchctl", "schtasks"}

// guardServiceManagers puts a directory with a failing stub of each service manager first on PATH, so that a test that
// reaches the real `systemctl --user restart monoagent-daemon` (or its launchd and Windows kin) by mistake gets an error and
// leaves its argv in a log. The returned check, called after the tests have run, reports what was called ("" when nothing was);
// Main fails the run on it, since a test that ignores the error would otherwise pass. A test that stubs a service manager
// itself puts its own directory before this one, and the guard only catches what falls through. Not on Windows, where a stub
// would have to be an executable of its own.
func guardServiceManagers() (check func() string, cleanup func()) {
	none := func() string { return "" }
	if runtime.GOOS == "windows" {
		return none, func() {}
	}
	dir, err := os.MkdirTemp("", "monoagent-noservice-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testhome:", err)
		return none, func() {}
	}
	log := filepath.Join(dir, "calls.log")
	for _, name := range serviceManagers {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> '" + log + "'\necho 'testhome: a test ran the real " + name + "; inject a fake service manager' >&2\nexit 1\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "testhome:", err)
			return none, func() { os.RemoveAll(dir) }
		}
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
			b, _ := os.ReadFile(log)
			return strings.TrimSpace(string(b))
		},
		func() { os.RemoveAll(dir) }
}
