// Package procchain answers "is the calling process this process, or one of its descendants?"
// for the tokens that are bound to the process a host or conductor started (the mission tokens
// of internal/agenttokens). Linux only: elsewhere Supported is false and a binding is not
// recorded or checked. internal/coderhost keeps its own copy of this logic for its session
// tokens.
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

// Verdict of a descent check.
type Verdict int

const (
	// Inside: the caller is the process (same start stamp) or a descendant of it.
	Inside Verdict = iota
	// Outside: the chain was read to its end and the process is not in it, or its pid is held by
	// a process with another start stamp (pid reuse).
	Outside
	// Unknown: the chain could not be read to its end and does not show the process.
	Unknown
)

// DescendsFrom says whether the calling process is the process pid (start stamp stamp) or one
// of its descendants.
func DescendsFrom(pid int, stamp string) Verdict {
	if pid <= 1 {
		return Outside
	}
	chain, ok := Chain()
	for _, p := range chain {
		if p == pid {
			if stamp == "" || Stamp(p) == stamp {
				return Inside
			}
			return Outside
		}
	}
	if !ok {
		return Unknown
	}
	return Outside
}

// Stamp identifies a process across pid reuse; "" when it cannot be read or is not supported.
func Stamp(pid int) string { return procStamp(pid) }
