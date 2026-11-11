package stop

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"syscall"
)

// sessGroup is the Group of a proven session unit: one flat level (sub ""), whose members are recomputed from the
// ledger and a fresh scan on every call, so a process that forks while the unit is ended is seen.
//
// A process that was a member stays one for as long as it lives with the start stamp it had (known): a member that
// outlives its parent is re-parented to init and no longer reaches the unit through the parent chain, and a survivor
// must not drop out of the list because its parent died first.
type sessGroup struct {
	c   sessContainment
	rec UnitRecord

	mu    sync.Mutex
	known map[int]string // pid -> start stamp of every process this group has seen as a member
}

// Flat says the unit has no sub-units: Stop cannot spare the commands of a begun operation inside it.
func (*sessGroup) Flat() bool { return true }

func (g *sessGroup) Identity() (CgroupIdentity, error) {
	return CgroupIdentity{ID: g.rec.CgroupID}, nil
}
func (g *sessGroup) Subgroups() ([]string, error) { return []string{""}, nil }
func (g *sessGroup) Close() error                 { return nil }

// members is the unit's membership now; an ambiguous scan is an error (nothing is signalled on it).
func (g *sessGroup) members() (sessView, error) {
	v, err := g.c.view(g.rec)
	if err != nil {
		return v, err
	}
	if v.why != "" {
		return v, fmt.Errorf("%w: %s", ErrNotProven, v.why)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.known == nil {
		g.known = map[int]string{}
	}
	have := map[int]bool{}
	for _, m := range v.members {
		have[m.PID] = true
	}
	for _, p := range v.all {
		if st, ok := g.known[p.PID]; ok && st == p.Stamp && !have[p.PID] {
			v.members = append(v.members, p)
		}
	}
	sort.Slice(v.members, func(i, j int) bool { return v.members[i].PID < v.members[j].PID })
	for _, m := range v.members {
		g.known[m.PID] = m.Stamp
	}
	return v, nil
}

func (g *sessGroup) Procs(string) ([]int, error) {
	v, err := g.members()
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(v.members))
	for _, m := range v.members {
		out = append(out, m.PID)
	}
	return out, nil
}

func (g *sessGroup) ProcInfo(_ string, pid int) (string, string, error) {
	p, err := g.c.os.Info(pid)
	return p.Stamp, p.Comm, err
}

func (g *sessGroup) Populated(string) (bool, error) {
	v, err := g.members()
	return len(v.members) > 0, err
}

// SurvivorWhy is the reason EndUnit gives for a member that is still there after the kill, when it has no better one.
func (*sessGroup) SurvivorWhy() string { return "stayed in the session after KILL" }

// SurvivorWhyFor says why a given member is still there: a process of another user cannot be signalled, any other
// stayed after the kill.
func (g *sessGroup) SurvivorWhyFor(pid int) string {
	if p, err := g.c.os.Info(pid); err == nil && p.UID != g.c.os.CurrentUID() {
		return fmt.Sprintf("could not be signalled (it belongs to uid %d)", p.UID)
	}
	return g.SurvivorWhy()
}

// signal re-reads the process right before signalling it: a pid that is gone, was reused (another start stamp) or has
// moved (another parent or session than the scan saw) is skipped, to be judged again by the next scan. Like every
// pid-based kill without a handle, the re-read and the signal are not atomic; the window is a few instructions.
func (g *sessGroup) signal(m SessProc, sig syscall.Signal) error {
	cur, err := g.c.os.Info(m.PID)
	if err != nil || cur.Stamp != m.Stamp || cur.SID != m.SID || cur.PPID != m.PPID {
		return nil
	}
	return g.c.os.Signal(m.PID, sig)
}

// Signal signals one member after checking that it is one now.
func (g *sessGroup) Signal(_ string, pid int, sig syscall.Signal) error {
	v, err := g.members()
	if err != nil {
		return err
	}
	for _, m := range v.members {
		if m.PID == pid {
			return g.signal(m, sig)
		}
	}
	return fmt.Errorf("process %d is not a member of the unit", pid)
}

// Kill ends every member: freeze (SIGSTOP) the members, rescan, freeze what appeared, until a pass finds nothing new
// (a frozen process cannot fork: this is the brake on the fork race), then SIGKILL. A whole process group whose every
// process is a member is killed by its group number (atomic); everything else by pid. The first error is returned
// after every member was tried; what is still there afterwards is the caller's survivors list.
func (g *sessGroup) Kill(string) error {
	var first error
	note := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	frozen := map[int]string{}
	var v sessView
	for round := 0; ; round++ {
		var err error
		if v, err = g.members(); err != nil {
			return err
		}
		fresh := false
		for _, m := range v.members {
			if frozen[m.PID] == m.Stamp {
				continue
			}
			fresh = true
			frozen[m.PID] = m.Stamp
			note(g.signal(m, sessSigStop))
		}
		if !fresh {
			break
		}
		if round+1 >= sessKillRounds {
			// still forking after the bound: the kill goes on with what is known, and what appears later is a survivor
			note(errors.New("processes were still appearing after the freeze passes"))
			break
		}
	}
	groupMembers := map[int]int{} // pgid -> processes of that group in the scan
	for _, p := range v.all {
		groupMembers[p.PGID]++
	}
	inGroup := map[int]int{} // pgid -> members of the unit in it
	for _, m := range v.members {
		inGroup[m.PGID]++
	}
	done := map[int]bool{}
	for _, m := range v.members {
		if m.PGID > 1 && inGroup[m.PGID] == groupMembers[m.PGID] && !done[m.PGID] && g.groupStillOurs(v.members, m.PGID) {
			done[m.PGID] = true
			note(g.c.os.SignalGroup(m.PGID, syscall.SIGKILL))
		}
	}
	for _, m := range v.members {
		note(g.signal(m, syscall.SIGKILL))
	}
	return first
}

// groupStillOurs re-reads every member of the group right before it is signalled by number: the number may have moved
// to a stranger since the scan, and then the members are signalled one by one (each re-read again) instead.
func (g *sessGroup) groupStillOurs(members []SessProc, pgid int) bool {
	for _, m := range members {
		if m.PGID != pgid {
			continue
		}
		cur, err := g.c.os.Info(m.PID)
		if err != nil || cur.Stamp != m.Stamp || cur.PGID != pgid {
			return false
		}
	}
	return true
}

// Rmdir is the "unit is empty" step: with no member left there is nothing else to end (the unit has no anchor), and a
// member that is still listed is a refusal. A signal is delivered asynchronously, which EndUnit's own wait covers.
func (g *sessGroup) Rmdir(string) error {
	v, err := g.members()
	if err != nil {
		return err
	}
	if len(v.members) > 0 {
		return fmt.Errorf("the unit still holds %d process(es)", len(v.members))
	}
	return nil
}
