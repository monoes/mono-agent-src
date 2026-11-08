package procchain

import (
	"errors"
	"strconv"
)

// The decisions of the Windows ancestry reader, kept apart from the system calls so they are tested on every OS.
//
// A Windows process records the number of the process that created it and keeps it after that process has exited,
// and the number can then be given to a newer, unrelated process. So a parent is believed only when it is a process
// that already existed when the child was created: its creation time is not later than the child's. A parent that
// cannot be opened, or that is younger than its child, ends the chain unread, which every caller treats as a no.

// procInfo is what one process shows about itself.
type procInfo struct {
	parent  int    // the creating process's number, as the process recorded it
	created uint64 // creation time, in 100 ns ticks (FILETIME), not counted from boot, so a reboot never repeats it
	exited  bool   // the process has ended (its object is still there because a handle to it is held)
}

var errUnreadable = errors.New("procchain: the process cannot be read")

// stampOf is the stamp of a process: "start:<creation time>", "" for one that has exited or cannot be read (an
// ended process is never taken for the live one that held its number).
func stampOf(read func(pid int) (procInfo, error), pid int) string {
	i, err := read(pid)
	if err != nil || i.exited || i.created == 0 {
		return ""
	}
	return "start:" + strconv.FormatUint(i.created, 10)
}

// parentOf is the number of pid's parent when the parent is proven to be the process that created pid.
func parentOf(read func(pid int) (procInfo, error), pid int) (int, bool) {
	child, err := read(pid)
	if err != nil || child.created == 0 || child.parent <= 0 || child.parent == pid {
		return 0, false
	}
	parent, err := read(child.parent)
	if err != nil || parent.created == 0 || parent.created > child.created {
		return 0, false // gone, unreadable, or a younger process that took the number of the real parent
	}
	// the child is read again: it must still name the same parent (nothing re-parents a Windows process, so a
	// difference is a read of another process under the same number)
	again, err := read(pid)
	if err != nil || again.parent != child.parent || again.created != child.created {
		return 0, false
	}
	return child.parent, true
}
