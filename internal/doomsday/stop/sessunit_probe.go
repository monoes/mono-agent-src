package stop

import (
	"errors"
	"fmt"
	"strconv"
)

// The portable half of the macOS OS layer (sessunit_darwin.go): the stamp format, the command check and the verdicts of
// the on-machine probe. They need no OS, so they are tested everywhere; the processes they judge are made and read only on
// macOS.

// fmtSessStamp is the start stamp of every path (Scan, Info, the host's pin): "<seconds>.<microseconds>", the
// microseconds zero padded to six digits (parseSessStamp is its inverse).
func fmtSessStamp(sec int64, usec int64) string {
	return strconv.FormatInt(sec, 10) + "." + fmt.Sprintf("%06d", usec)
}

// sessProbeShape judges the assumptions of the session unit on the processes the probe made: leader (started with its
// own session by this process, self), plain (a child of the leader that changed nothing), grp (a child that made a process
// group of its own) and own (a child that made a session of its own). Every refusal names the assumption that failed,
// because it is the reason shown for the mode being unavailable on this machine.
func sessProbeShape(self int, leader, plain, grp, own SessProc) error {
	switch {
	case leader.SID != leader.PID:
		return fmt.Errorf("a process started with its own session has session %d, not its own number %d: session numbers are not leader pids on this machine", leader.SID, leader.PID)
	case leader.PGID != leader.PID:
		return fmt.Errorf("a session leader has process group %d, not its own number %d", leader.PGID, leader.PID)
	case leader.PPID != self:
		return fmt.Errorf("the probe's leader has parent %d, not this process (%d)", leader.PPID, self)
	case plain.SID != leader.PID || plain.PPID != leader.PID:
		return fmt.Errorf("a plain child of a session leader is in session %d under parent %d, not in session %d under %d", plain.SID, plain.PPID, leader.PID, leader.PID)
	case grp.PGID != grp.PID || grp.SID != leader.PID:
		return fmt.Errorf("a child that made a process group of its own is in group %d, session %d: expected group %d and session %d (the session must outlive job control)", grp.PGID, grp.SID, grp.PID, leader.PID)
	case own.SID != own.PID || own.PPID != leader.PID:
		return fmt.Errorf("a child that made its own session is in session %d under parent %d: expected session %d under %d", own.SID, own.PPID, own.PID, leader.PID)
	}
	for _, p := range []SessProc{leader, plain, grp, own} {
		if _, ok := parseSessStamp(p.Stamp); !ok {
			return fmt.Errorf("process %d has a start time that cannot be used (%q)", p.PID, p.Stamp)
		}
		if p.UID != leader.UID {
			return fmt.Errorf("process %d runs as uid %d, not the leader's %d", p.PID, p.UID, leader.UID)
		}
	}
	return nil
}

// sessProbeFinds judges what a scan must show of the probe's processes: each listed with the same facts Info gave, and the
// unit's membership rules (resolveSession over the scan with the leader recorded) tying all four to the leader.
func sessProbeFinds(scan []SessProc, leader SessProc, want ...SessProc) error {
	byPID := map[int]SessProc{}
	for _, p := range scan {
		byPID[p.PID] = p
	}
	for _, w := range want {
		got, ok := byPID[w.PID]
		switch {
		case !ok:
			return fmt.Errorf("the process scan does not list process %d", w.PID)
		case got.Stamp != w.Stamp:
			return fmt.Errorf("process %d has start %q in the scan and %q when read alone: a recorded leader could not be matched", w.PID, got.Stamp, w.Stamp)
		case got.SID != w.SID || got.PGID != w.PGID || got.PPID != w.PPID:
			return fmt.Errorf("process %d is read differently by the scan (session %d, group %d, parent %d) and alone (%d, %d, %d)", w.PID, got.SID, got.PGID, got.PPID, w.SID, w.PGID, w.PPID)
		}
	}
	res := resolveSession(scan, []Leader{{PID: leader.PID, Stamp: leader.Stamp}}, false, nil)
	if res.why != "" {
		return errors.New(res.why)
	}
	members := map[int]bool{}
	for _, m := range res.members {
		members[m.PID] = true
	}
	for _, w := range want {
		if !members[w.PID] {
			return fmt.Errorf("process %d is not found as a member of the leader's session unit", w.PID)
		}
	}
	return nil
}
