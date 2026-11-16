package update

import (
	"testing"
)

func TestManualUndoCommandByOS(t *testing.T) {
	if got := manualUndoCommand("linux", "/b/x.bak", "/b/x"); got != "mv -f /b/x.bak /b/x" {
		t.Errorf("linux = %q", got)
	}
	if got := manualUndoCommand("darwin", "/b/x y.bak", "/b/x"); got != "mv -f '/b/x y.bak' /b/x" {
		t.Errorf("darwin = %q", got)
	}
	got := manualUndoCommand("windows", `C:\b\x y.bak`, `C:\b\x.exe`)
	if got != `move /Y "C:\b\x y.bak" C:\b\x.exe` {
		t.Errorf("windows = %q", got)
	}
}

// A GOOS that is not windows is a POSIX shell: mv, quoted when needed.
func TestManualUndoCommandUnknownGOOSIsPosix(t *testing.T) {
	for _, goos := range []string{"freebsd", ""} {
		if got := manualUndoCommand(goos, "/b/x y.bak", "/b/x"); got != "mv -f '/b/x y.bak' /b/x" {
			t.Errorf("%q = %q", goos, got)
		}
	}
}
