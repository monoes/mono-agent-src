//go:build windows

package update

import (
	"os"
	"os/exec"
)

// Windows has no owner check that means the same: nothing there is run to ask its version, and nothing is replaced from
// here; every stale CLI is only reported.
func ownedByUser(fi os.FileInfo) bool       { return false }
func ownedByUserOrRoot(fi os.FileInfo) bool { return false }

var isRoot = func() bool { return false }

func ownProcessGroup(cmd *exec.Cmd) func() { return func() {} }
