package dynorg

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Settings every git call of the conductor carries on its command line.
// A -c setting overrides the repository-local configuration, so these hold
// whatever that file says at the moment git runs. They cover what git has a
// switch for: the file system monitor and the hooks directory (and, in
// gitEnv, the lazy fetch of missing objects). Content filters and merge
// drivers have no such switch, which is why refuseProgramConfig checks for
// them (and why that check is best effort against a file rewritten at the
// same moment).

// noHooksPath is where core.hooksPath points on systems that have a null
// device: git finds no hook there, and no folder exists that anyone could
// list or write.
const noHooksPath = "/dev/null"

// emptyHooksDir is the hooks directory for the conductor's git calls. On
// Windows, which has no such device, it is a folder under the operator's own
// state folder (its mode bits do not protect it there; see SECURITY.md), checked to be empty on every use; if
// anything is in it the operation is refused. It is never derived from a
// relative path or a temporary folder, and a failure is not remembered.
func emptyHooksDir() (string, error) {
	if runtime.GOOS != "windows" {
		return noHooksPath, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", fmt.Errorf("the operator's state folder is unknown")
	}
	dir := filepath.Join(home, ".monoagent", "nohooks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) != 0 {
		return "", fmt.Errorf("%s is not empty", dir)
	}
	return dir, nil
}

// gitPins are the -c arguments that precede every conductor git command.
func gitPins() ([]string, error) {
	hooks, err := emptyHooksDir()
	if err != nil {
		return nil, fmt.Errorf("no empty hooks directory could be used, so git was not run: %v", err)
	}
	return []string{"-c", "core.hooksPath=" + hooks, "-c", "core.fsmonitor=false",
		// Merges and checkouts never descend into submodules.
		"-c", "submodule.recurse=false"}, nil
}
