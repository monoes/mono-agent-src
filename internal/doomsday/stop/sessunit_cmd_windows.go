//go:build windows

package stop

import (
	"errors"
	"os/exec"
)

// checkSessCmd refuses on Windows: the session unit exists only on macOS.
func checkSessCmd(*exec.Cmd) error { return errors.New("the session unit is only available on macOS") }
