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

// members is the unit's membership now: the trusted members and the processes that cannot be tied to the unit. The
// signaller and its ancestors are never in either (Prove already refuses a unit that holds the signaller; this is the
// same rule again at signal time), and a member seen before stays one while it lives with the stamp it had.
func (g *sessGroup) members() (sessView, error) {
	g.mu.Lock()
	seen := make(map[int]string, len(g.known))
	for pid, st := range g.known {
		seen[pid] = st
	}
	g.mu.Unlock()
	v, err := g.c.viewSeen(g.rec, seen)
	if err != nil {
		return v, err
	}
	if v.why != "" {
		return v, fmt.Errorf("%w: %s", ErrNotProven, v.why)
	}
	self, _, err := g.c.os.Self()
	if err != nil {
		return v, fmt.Errorf("%w: the signaller's own identity is unknown: %v", ErrNotProven, err)
	}
	spared := map[int]bool{}
	byPID := map[int]SessProc{}
	for _, p := range v.all {
		byPID[p.PID] = p
	}
	for pid := self; pid > 1 && !spared[pid]; pid = byPID[pid].PPID {
		spared[pid] = true
	}
	keep := func(in []SessProc) []SessProc {
		var out []SessProc
		for _, p := range in {
			if !spared[p.PID] {
				out = append(out, p)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
		return out
	}
	v.members, v.unproven = keep(v.members), keep(v.unproven)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.known == nil {
		g.known = map[int]string{}
	}
	for _, m := range v.members {
		g.known[m.PID] = m.Stamp
	}
	return v, nil
}

// Procs lists every process the unit still holds: the members, then the processes that cannot be tied to it. The second
// kind is never signalled (Signal and Kill refuse it); it is listed so the end reports it as a survivor.
func (g *sessGroup) Procs(string) ([]int, error) {
	v, err := g.members()
	if err != nil {
		return nil, err
	}
	out := make([]int, 0, len(v.members)+len(v.unproven))
	for _, m := range v.members {
		out = append(out, m.PID)
	}
	for _, m := range v.unproven {
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
	return len(v.members)+len(v.unproven) > 0, err
}

// SurvivorWhy is the reason EndUnit gives for a member that is still there after the kill, when it has no better one.
func (*sessGroup) SurvivorWhy() string { return "stayed in the session after KILL" }

// SurvivorWhyFor says why a given member is still there: a process of another user cannot be signalled, any other
// stayed after the kill.
func (g *sessGroup) SurvivorWhyFor(pid int) string {
	if v, err := g.members(); err == nil {
		for _, u := range v.unproven {
			if u.PID == pid {
				return "was not signalled: " + v.unprovenWhy
			}
		}
	}
	if p, err := g.c.os.Info(pid); err == nil && p.UID != g.c.os.CurrentUID() {
		return fmt.Sprintf("could not be signalled (it belongs to uid %d)", p.UID)
	}
	return g.SurvivorWhy()
}

// signal re-reads the process right before signalling it: a pid that is gone or whose start stamp changed (the number was
// reused) is skipped. Identity is the pid and its start stamp and nothing else: a member that was re-parented or moved to
// a session of its own since the scan is still the process that was seen as a member. Like every pid-based kill without a
// handle, the re-read and the signal are not atomic; the window is a few instructions.
func (g *sessGroup) signal(m SessProc, sig syscall.Signal) error {
	cur, err := g.c.os.Info(m.PID)
	switch {
	case err == nil:
		if cur.Stamp != m.Stamp {
			return nil
		}
	case errors.Is(err, errSessGone):
		return nil
	default:
		// the read failed: that is not evidence the member is gone (a member must not be able to starve the kill by making
		// itself hard to read), so the plain start stamp is the identity, and without one the failure is returned, not hidden
		sr, ok := g.c.os.(stampReader)
		if !ok {
			return fmt.Errorf("member %d cannot be read before the signal: %w", m.PID, err)
		}
		st, serr := sr.StampOf(m.PID)
		switch {
		case errors.Is(serr, errSessGone):
			return nil
		case serr != nil:
			return fmt.Errorf("member %d cannot be read before the signal: %w", m.PID, serr)
		case st != m.Stamp:
			return nil
		}
	}
	return g.c.os.Signal(m.PID, sig)
}

// killRecorded is what Stop does when the unit's processes cannot be listed at all: it does not give up. Every process
// this group has seen as a member and every recorded leader is signalled by pid and start stamp (the identity the ledger
// and the earlier reads gave), nothing else is touched, and the answer is NOT PROVEN: the unit may hold more.
func (g *sessGroup) killRecorded(cause error) error {
	ids := map[int]string{}
	g.mu.Lock()
	for pid, st := range g.known {
		ids[pid] = st
	}
	g.mu.Unlock()
	if g.c.led != nil {
		if ls, err := g.c.led.Leaders(g.rec.Generation); err == nil {
			for _, l := range ls {
				if l.Unit == g.rec.CgroupID {
					ids[l.PID] = l.Stamp
				}
			}
		}
	}
	for pid, st := range ids {
		_ = g.signal(SessProc{PID: pid, Stamp: st}, sessSigStop)
	}
	for pid, st := range ids {
		_ = g.signal(SessProc{PID: pid, Stamp: st}, syscall.SIGKILL)
	}
	return fmt.Errorf("%w: the unit's processes could not be listed (%v); the %d recorded member(s) were signalled by pid and start stamp, nothing else", ErrNotProven, cause, len(ids))
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
	frozen := map[int]SessProc{} // every member that was stopped: all of them are killed, whatever happens next
	killFrozen := func() {
		for _, m := range frozen {
			note(g.signal(m, syscall.SIGKILL))
		}
	}
	var v sessView
	for round := 0; ; round++ {
		var err error
		if v, err = g.members(); err != nil {
			killFrozen() // a read that fails after the freeze must not leave the unit stopped
			return g.killRecorded(err)
		}
		fresh := false
		for _, m := range v.members {
			if f, ok := frozen[m.PID]; ok && f.Stamp == m.Stamp {
				continue
			}
			fresh = true
			frozen[m.PID] = m
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
	killFrozen() // whatever was stopped is killed on every path out, not only on a failed read
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
	if n := len(v.members) + len(v.unproven); n > 0 {
		return fmt.Errorf("the unit still holds %d process(es)", n)
	}
	return nil
}
