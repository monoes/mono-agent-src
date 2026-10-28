package coderhost

import "os"

const maxAncestors = 64

// callerChain lists this process and its parent chain, nearest first; ok is false when the chain
// cannot be read to its end (pid 1 or the top). A variable so tests can stand in for a caller.
var callerChain = func() (pids []int, ok bool) {
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

// descendsFrom says whether the calling process is the process pid (with the start stamp
// stamp) or one of its descendants. Workers a turn starts are its descendants and keep working;
// a sibling process that merely read the turn's environment is not.
func descendsFrom(pid int, stamp string) bool {
	if pid <= 1 {
		return false
	}
	chain, _ := callerChain()
	for _, p := range chain {
		if p == pid {
			return stamp == "" || ProcStamp(p) == stamp
		}
	}
	return false
}
