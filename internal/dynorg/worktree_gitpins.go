package dynorg

import (
	"fmt"
	"os"
	"sync"
)

// Settings every git call of the conductor carries on its command line.
// A -c setting overrides the repository-local configuration, so these hold
// whatever that file says at the moment git runs. They cover what git has a
// switch for: the file system monitor and the hooks directory. Content
// filters and merge drivers have no such switch, which is why
// refuseProgramConfig checks for them (and why that check is best effort
// against a file rewritten at the same moment).

var (
	noHooksOnce sync.Once
	noHooksDir  string
	noHooksErr  error
)

// emptyHooksDir is an empty directory only this process knows, used as the
// hooks directory so the repository's hook scripts never run for the
// conductor's own git calls. It lives until the process ends.
func emptyHooksDir() (string, error) {
	noHooksOnce.Do(func() {
		noHooksDir, noHooksErr = os.MkdirTemp("", "monoagent-nohooks-")
	})
	return noHooksDir, noHooksErr
}

// gitPins are the -c arguments that precede every conductor git command.
func gitPins() ([]string, error) {
	hooks, err := emptyHooksDir()
	if err != nil {
		return nil, fmt.Errorf("no empty hooks directory could be made, so git was not run: %v", err)
	}
	return []string{"-c", "core.hooksPath=" + hooks, "-c", "core.fsmonitor=false"}, nil
}
