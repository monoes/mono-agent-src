package coderhost

import "github.com/monoes/mono-agent/internal/procchain"

// ancestrySupported: process ancestry can be read, so a session token is bound to its turn's
// process. Where it is false a token is checked by its signature and turn only (documented
// fallback, see sessiontoken.go).
const ancestrySupported = procchain.Supported

// ProcStamp identifies a process across pid reuse ("" when unreadable or unsupported).
func ProcStamp(pid int) string { return procchain.Stamp(pid) }

// BoundStamp is the stamp a bound (v2) turn's worker is recorded with: the start stamp and the boot id, so a process of a
// later boot with the same pid and start tick is not taken for it.
func BoundStamp(pid int) string { return procchain.BootStamp(pid) }

// callerChain lists this process and its parent chain, nearest first; ok is false when the chain
// cannot be read to its end. A variable so tests can stand in for a caller.
var callerChain = func() ([]int, bool) { return procchain.Chain() }

// descendsFrom says whether the calling process is the process pid (with the start stamp
// stamp) or one of its descendants. An unreadable chain that does not show it is a no.
func descendsFrom(pid int, stamp string) bool {
	chain, _ := callerChain()
	return procchain.InChain(chain, pid, stamp)
}
