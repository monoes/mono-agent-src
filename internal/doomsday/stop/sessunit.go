package stop

import (
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// THE SESSION UNIT (the macOS unit of #1582 option B). A run's unit is "the sessions the host starts": every turn
// leader (the process the host starts for a boss or child turn) is born its own session leader (setsid), and the host
// records it (pid + start stamp) in the witness BEFORE the turn is released (Adopter.Adopt, sessunit_create.go). The unit
// is the set of processes whose session is a recorded leader's, plus any live process whose parent chain reaches one
// of them (a child that made a session of its own is still a child). No process is ever joined to anything across
// sessions, so the two causes that stopped a process-group unit from holding a turn (a group made in another
// session, a turn that is a session leader) do not arise.
//
// Everything OS specific is behind SessionOS; this file and sessunit_create.go are portable, so the logic is tested on
// Linux against a fake (sessunit_*_test.go). The macOS binding (kern.proc.all, getsid) is slice S2; until it exists
// realSessionOS is nil everywhere and the kind refuses, and platformKinds still names the process-group unit for macOS.
//
// The guarantee is the honest one of the Windows unit: a process that made a session of its own AND is no longer a
// child of any member (daemonised, double-forked with setsid, or started through launchd, open or XPC) is NOT seen and
// NOT covered. Every end of such a unit is UnitBestEffort and the report says so ("stopped (best effort: ...)"), never a
// bare "stopped". The limit is the same in every build, so it is stated once here: SessionBestEffortNote.
//
// A same-user process can already signal every other process of that user: the unit is an honesty and cleanup
// mechanism, not a privilege boundary, and it never signals a process it cannot tie to a recorded leader.

// KindSession is the UnitRecord.Kind of the session unit. Best effort (see UnitBestEffort).
const KindSession = "psession"

// SessionBestEffortNote is the limit of a session unit's end, carried in UnitOutcome.Note of every end of this kind.
const SessionBestEffortNote = "best effort: the run's process sessions and their child processes were ended; a process that detached into a session of its own, or was started through launchd, open or XPC, is not covered"

// Tunables of the stop; tests lower sessKillRounds to see the bound.
var sessKillRounds = 8 // freeze passes before the kill: a process forking faster than this is reported as a survivor

// SessProc is one live (not zombie) process as the scan sees it.
type SessProc struct {
	PID, PPID, PGID, SID int
	// Stamp is the process start time as "<seconds>.<microseconds>" (microseconds zero padded to six digits): the pid is
	// only an identity together with it.
	Stamp string
	UID   uint32
	Comm  string
}

// SessionOS is the seam over the OS (contain_darwin.go in production from slice S2, a fake in tests).
type SessionOS interface {
	BootID() (string, error)
	// Self is the pid and start stamp of the caller.
	Self() (pid int, stamp string, err error)
	CurrentUID() uint32
	// Scan lists every live, non-zombie process the caller can see, each with its session id. It is unprivileged.
	Scan() ([]SessProc, error)
	// Info re-reads one process; an error for a process that is absent or only a zombie.
	Info(pid int) (SessProc, error)
	// Signal signals one process; a process that is already gone is not an error. Never pid <= 1.
	Signal(pid int, sig syscall.Signal) error
	// SignalGroup signals a whole process group (kill(-pgid)); a group that is already gone is not an error. Never pgid <= 1.
	SignalGroup(pgid int, sig syscall.Signal) error
	// CheckCmd refuses a command that would not start as a session leader of its own (placement changes nothing).
	CheckCmd(cmd *exec.Cmd) error
	// SelfTest creates a session leader with a setpgid child and a setsid child, finds all three, ends them and checks
	// they are gone: the probe step "session create, find and kill".
	SelfTest() error
}

// Leader is one session leader the host recorded before its turn was released.
type Leader struct {
	PID   int    `json:"pid"`
	Stamp string `json:"stamp"`
	At    int64  `json:"at"` // unix seconds, informational
}

// LeaderLedger is the append-only, authenticated list of a unit's leaders. In production it is the witness
// (Sequencer.AddLeader / Leaders); a read that cannot be completed is an error, never an empty list.
type LeaderLedger interface {
	Leaders(gen uint64) ([]Leader, error)
	AddLeader(gen uint64, l Leader) error
}

type ledgerKernel struct {
	Kernel
	LeaderLedger
}

// WithLeaderLedger is the Kernel the session unit reads its leaders from: k for everything else, l for the ledger. A
// Kernel without a ledger cannot prove, place into or adopt into a session unit.
func WithLeaderLedger(k Kernel, l LeaderLedger) Kernel {
	return ledgerKernel{Kernel: k, LeaderLedger: l}
}

// sessHandle is UnitRecord.Handle of a session unit: the creator's identity (the `doomsday start` process, which is
// not the host and exits). The unit has no anchor process: the record and the ledger are the unit.
type sessHandle struct {
	CreatorPID   int    `json:"creator_pid"`
	CreatorStamp string `json:"creator_stamp"`
}

func parseSessStamp(s string) (int64, bool) {
	i := strings.IndexByte(s, '.')
	if i <= 0 || i == len(s)-1 {
		return 0, false
	}
	sec, e1 := strconv.ParseInt(s[:i], 10, 64)
	us, e2 := strconv.ParseInt(s[i+1:], 10, 64)
	if e1 != nil || e2 != nil || sec < 0 || us < 0 || us >= 1_000_000 {
		return 0, false
	}
	return sec*1_000_000 + us, true
}

// sessResolution is the unit's membership at one scan.
type sessResolution struct {
	members []SessProc // sorted by pid
	live    []int      // recorded leaders alive with their recorded start stamp
	// why is non-empty when the scan cannot be tied to the ledger (a leader's number held by another process, a boot
	// time that differs while the number is in use, a leader that does not lead its session): nothing is signalled.
	why string
}

// resolveSession computes the members of the recorded leaders' sessions in one scan. rebooted says the boot time of
// the unit's record differs from this machine's (not evidence by itself: the clock can be stepped).
//
// A session's number is its leader's pid. While the leader (pid and stamp) is alive its session is trusted. When it is
// gone, processes that still carry that session number are the orphans of a turn, but the number may also have been
// given to an unrelated session after the leader died; the scan trusts them only when the number is not held by another
// live process and no one of them is older than the leader. The kernel does not hand out a number that is still a
// session id (BSD behaviour; to be verified on the Mac in S2), which is why this is defence in depth and not the only
// protection. Anything ambiguous is "why", never a signal.
func resolveSession(procs []SessProc, leaders []Leader, rebooted bool) sessResolution {
	var res sessResolution
	byPID := make(map[int]SessProc, len(procs))
	bySID := map[int][]SessProc{}
	for _, p := range procs {
		byPID[p.PID] = p
		bySID[p.SID] = append(bySID[p.SID], p)
	}
	// one entry per session number: a number can be recorded twice (two turns whose leaders got the same pid)
	type slot struct {
		stamps []string
		min    int64
	}
	slots := map[int]*slot{}
	for _, l := range leaders {
		us, ok := parseSessStamp(l.Stamp)
		if l.PID <= 1 || !ok {
			res.why = fmt.Sprintf("the ledger names a leader that cannot be checked (pid %d, start %q)", l.PID, l.Stamp)
			return res
		}
		s := slots[l.PID]
		if s == nil {
			s = &slot{min: us}
			slots[l.PID] = s
		}
		s.stamps = append(s.stamps, l.Stamp)
		if us < s.min {
			s.min = us
		}
	}
	member := map[int]SessProc{}
	var pids []int
	for pid := range slots {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	for _, pid := range pids {
		s := slots[pid]
		cur, held := byPID[pid]
		alive := false
		if held {
			for _, st := range s.stamps {
				alive = alive || cur.Stamp == st
			}
		}
		inSession := bySID[pid]
		switch {
		case rebooted && len(inSession) > 0:
			res.why = fmt.Sprintf("the boot time differs from the unit's and session number %d is in use by %d process(es) that are not provably this unit's (after a reboot the number may belong to another process: end them by hand)", pid, len(inSession))
			return res
		case alive:
			if cur.SID != pid {
				res.why = fmt.Sprintf("the recorded leader %d does not lead its session", pid)
				return res
			}
			res.live = append(res.live, pid)
		case len(inSession) == 0:
			continue // the leader and its whole session are gone
		case held:
			res.why = fmt.Sprintf("the recorded leader %d is gone and its number is held by another process that is in session %d, so the session is not tied to this unit", pid, pid)
			return res
		}
		for _, p := range inSession {
			us, ok := parseSessStamp(p.Stamp)
			if !ok || us < s.min {
				res.why = fmt.Sprintf("process %d in session %d is older than the unit's leader, so it is not provably this unit's", p.PID, pid)
				return res
			}
			member[p.PID] = p
		}
	}
	// a child of a member is a member, in whatever session it has made for itself
	kids := map[int][]SessProc{}
	for _, p := range procs {
		kids[p.PPID] = append(kids[p.PPID], p)
	}
	queue := make([]int, 0, len(member))
	for pid := range member {
		queue = append(queue, pid)
	}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, k := range kids[pid] {
			if k.PID <= 1 {
				continue
			}
			if _, seen := member[k.PID]; !seen {
				member[k.PID] = k
				queue = append(queue, k.PID)
			}
		}
	}
	for _, p := range member {
		res.members = append(res.members, p)
	}
	sort.Slice(res.members, func(i, j int) bool { return res.members[i].PID < res.members[j].PID })
	return res
}

var errNoSessionOS = errors.New("the session mechanism is only available on macOS")
var errNoLedger = errors.New("no leader ledger is wired, so the unit's sessions are unknown")

// sessView is one read of the world for one unit: the scan, the ledger and the resolution of the two.
type sessView struct {
	all      []SessProc
	rebooted bool
	sessResolution
}

func (c sessContainment) view(rec UnitRecord) (sessView, error) {
	if c.os == nil {
		return sessView{}, errNoSessionOS
	}
	if c.led == nil {
		return sessView{}, errNoLedger
	}
	leaders, err := c.led.Leaders(rec.Generation)
	if err != nil {
		return sessView{}, fmt.Errorf("the leader ledger cannot be read: %w", err)
	}
	all, err := c.os.Scan()
	if err != nil {
		return sessView{}, fmt.Errorf("the processes cannot be listed: %w", err)
	}
	rebooted := false
	if boot, err := c.os.BootID(); err == nil && boot != rec.BootID {
		rebooted = true
	}
	return sessView{all: all, rebooted: rebooted, sessResolution: resolveSession(all, leaders, rebooted)}, nil
}
