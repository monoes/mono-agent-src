package stop

import (
	"encoding/json"
	"fmt"
)

// StrandedUnit says why the run's process group (the macOS unit) cannot take a turn from the running coder host, "" when it
// can or when that is not known. The group's anchor is started by the host that was running when the run began, so it is a
// child of that host; a host started later is in another session and cannot join the group (setpgid is refused across
// sessions), so every turn would fail closed. hostPID is the running host's pid, 0 when none runs, negative when unknown.
// It reads the unit file and the anchor's parent only, and changes nothing.
func StrandedUnit(dir RunDir, gen uint64, hostPID int) string {
	rec, ok, err := dir.ReadUnitFile(gen)
	if err != nil || !ok {
		return ""
	}
	return strandedUnitReason(rec, hostPID, realPPidOf)
}

func strandedUnitReason(rec UnitRecord, hostPID int, ppidOf func(pid int) (int, error)) string {
	if rec.Kind != KindPGroup || rec.AnchorPID <= 0 || hostPID < 0 || ppidOf == nil {
		return ""
	}
	var h pgHandle
	if json.Unmarshal([]byte(rec.Handle), &h) != nil {
		return ""
	}
	parent, err := ppidOf(rec.AnchorPID)
	if err != nil || parent == hostPID {
		return ""
	}
	return fmt.Sprintf("the run's process group was made by a coder host that is not the running one (the group's session %d is not this host's, and a process cannot join a group across sessions)", h.Sid)
}
