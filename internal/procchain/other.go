//go:build !linux

package procchain

// Supported is false where process ancestry is not read: a mission token is then checked by its
// signature and lifetime only (documented fallback, see agenttokens/mission_process.go).
const Supported = false

// procStamp is "" where ancestry is not read.
func procStamp(int) string { return "" }

func parentPID(int) (int, bool) { return 0, false }

func bootID() string { return "" }
