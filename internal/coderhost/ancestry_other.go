//go:build !linux

package coderhost

// ancestrySupported is false where process ancestry is not read: a session token is then checked
// by its signature and its turn only (documented fallback, see sessiontoken.go).
const ancestrySupported = false

// ProcStamp is "" where ancestry is not read.
func ProcStamp(int) string { return "" }

func parentPID(int) (int, bool) { return 0, false }
