// Package procchain answers "is the calling process this process, or one of its descendants?"
// for the tokens that are bound to the process a host or conductor started (the mission tokens
// of internal/agenttokens). Linux only: elsewhere Supported is false and a binding is not
// recorded or checked. internal/coderhost uses it for its session tokens.
package procchain

import "os"

const maxAncestors = 64

// Chain lists this process and its parent chain, nearest first; ok is false when the chain
// cannot be read to its end (pid 1 or the top). A variable so tests can stand in for a caller.
var Chain = func() (pids []int, ok bool) {
	pid := os.Getpid()
	for i := 0; i < maxAncestors && pid > 1; i++ {
		pids = append(pids, pid)
		p, readable := parentPID(pid)
		if !readable {
			return pids, false
		}
		pid = p
	}
	return pids, true
}

// InChain says whether chain (a caller's process and its parents) holds the process pid with
// the start stamp stamp. It fails closed: a chain that could not be read to its end is just the
// part that was read, and a pid that is not in it is a no, whatever the reason.
func InChain(chain []int, pid int, stamp string) bool {
	if pid <= 1 {
		return false
	}
	for _, p := range chain {
		if p == pid {
			return stamp == "" || Stamp(p) == stamp
		}
	}
	return false
}

// DescendsFrom says whether the calling process is the process pid (start stamp stamp) or one
// of its descendants. An unreadable chain that does not show it is a no (fails closed), so a
// caller that hides its ancestry (its own /proc, a race) is refused, not trusted.
func DescendsFrom(pid int, stamp string) bool {
	chain, _ := Chain()
	return InChain(chain, pid, stamp)
}

// Stamp identifies a process across pid reuse; "" when it cannot be read or is not supported.
func Stamp(pid int) string { return procStamp(pid) }
