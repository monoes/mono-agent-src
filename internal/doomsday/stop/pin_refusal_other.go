//go:build !windows

package stop

// pinRefusalExpected: off Windows nothing refuses a rename of a pinned file (the pin reports it afterwards), so any
// error from the change is a failure of the self-test, not a refusal.
func pinRefusalExpected(error) bool { return false }
