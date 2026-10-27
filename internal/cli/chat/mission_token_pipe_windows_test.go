//go:build windows

package chat

import "testing"

// The tests that hand a token over need a Unix descriptor and the shell-script
// fake CLIs; they do not run on Windows.
func handOffMissionToken(t *testing.T, _ string) {
	t.Helper()
	t.Skip("mission token handoff tests need Unix descriptors")
}
