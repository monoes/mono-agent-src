package orgdesign

import (
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/daemonhb"
)

// saveLockWait bounds how long Save waits for another writer of the same org file: a holder keeps
// the lock for one compare and one rename.
const saveLockWait = 10 * time.Second

// lockOrgFile serialises the writers of one org file (the GUI, the CLI and the chat's tools) so the
// compare against the loaded bytes and the rename are one step.
func lockOrgFile(path string) (release func(), err error) {
	deadline := time.Now().Add(saveLockWait)
	for {
		release, err = daemonhb.LockFile(path + ".lock")
		if !errors.Is(err, daemonhb.ErrHeld) {
			return release, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another process is saving %s; try again", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
