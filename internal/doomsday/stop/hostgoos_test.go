package stop

import "testing"

// The suite's fakes are Linux-shaped: every test runs as a Linux host unless it says otherwise, on any real OS.
func init() { hostGOOS = "linux" }

// hostAs makes the code under test believe it runs on goos for the rest of the test.
func hostAs(t *testing.T, goos string) {
	t.Helper()
	old := hostGOOS
	hostGOOS = goos
	t.Cleanup(func() { hostGOOS = old })
}
