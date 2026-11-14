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
// mechanism, not a privilege boundary. It signals only processes it ties to a recorded leader by session number, start
// time and parent chain; the one case that tie cannot cover (an unrelated session reusing a dead leader's number within the
// same boot) is stated with the residual risks of slice S1.

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
	// BootID must change on every boot and on nothing else: a stepped clock must not move it (macOS: kern.bootsessionuuid, not
	// kern.boottime). The rules of resolveSession take a difference as proof of a reboot.
	BootID() (string, error)
	// Hold keeps the number of pid reserved for as long as the returned release is not called, even after the process exits:
	// pid must be an unreaped child of the caller with that start stamp, and from here on only release reaps it (the caller
	// does not wait for it). An error means the number cannot be kept.
	Hold(pid int, stamp string) (release func(), err error)
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
	// Unit is the identity of the unit the leader joined (UnitRecord.CgroupID), so a second unit of the same generation
	// does not inherit the first one's leaders, and a unit that was ended takes no leader (ErrUnitEnded).
	Unit uint64 `json:"unit"`
	At   int64  `json:"at"` // unix seconds, informational
	// Empty is set by the ledger when the leader's session was seen gone (RetireLeader): the number is no longer the unit's.
	Empty bool `json:"empty,omitempty"`
}

// ErrUnitEnded means the ledger refused a leader because the unit's end was already recorded.
var ErrUnitEnded = errors.New("stop: the unit is sealed or already ended")

// LeaderLedger is the append-only, authenticated list of a unit's leaders. In production it is the witness
// (Sequencer.AddLeader / Leaders); a read that cannot be completed is an error, never an empty list.
type LeaderLedger interface {
	Leaders(gen uint64) ([]Leader, error)
	// AddLeader appends a leader; it refuses with ErrUnitEnded when the unit is sealed or its end line is written (the seal is
	// written under the same lock before the end proves the unit, so a leader is never recorded after the proof that missed it).
	AddLeader(gen uint64, l Leader) error
	// RetireLeader records that the leader's session was seen gone (idempotent). Leaders then returns it with Empty set.
	RetireLeader(gen uint64, l Leader) error
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
	members []SessProc // trusted: tied to a recorded leader; sorted by pid
	live    []int      // recorded leaders alive with their recorded start stamp
	// unproven are processes that carry a recorded session number but cannot be tied to the run (the number was reused, a
	// process is older than the leader): they are never signalled and are listed with unprovenWhy.
	unproven    []SessProc
	unprovenWhy string
	// gone are the recorded leaders (not yet retired) whose leader and whole session were seen gone in this scan: the caller
	// retires them, so a later holder of the number is never taken for the unit's.
	gone []Leader
	// why is non-empty when the ledger itself cannot be used (an entry that cannot be checked): nothing is resolved.
	why string
}

// resolveSession computes the members of the recorded leaders' sessions in one scan. rebooted says the boot time of
// the unit's record differs from this machine's (not evidence by itself: the clock can be stepped).
//
// A session's number is its leader's pid; each recorded number is judged on its own, so one ambiguous number never blocks
// the others. While the leader (pid and stamp) is alive its session is trusted. When it is gone, the processes that still
// carry the number are the orphans of a turn, but the number may also have been given to an unrelated session after the
// leader died. The scan then separates what cannot be the new holder's from what can:
//   - a process older than the leader cannot be this unit's session (unproven);
//   - the live holder of the number (another start stamp) that leads a session of that number, and its descendants, are
//     the holder's own and are left alone;
//   - a process started before such a holder cannot be the holder's: it is an orphan of the old session (trusted); one
//     started after it, outside the holder's descendants, cannot be told apart (unproven);
//   - after a boot time change the old processes cannot exist: the number's processes are another boot's and are left alone
//     (unless the recorded leader itself is alive with its exact stamp, which rules out a reboot).
//
// seen are processes (pid -> start stamp) that an earlier read of the same end saw as members: they stay members while
// they live with that stamp, and their children are members, though their parent has died since.
//
// The kernel does not hand out a number that is still a process-group id (BSD behaviour), and a session's leader group
// pins it while any process stays in it; both are to be verified on the Mac in S2, and the rules above are the defence
// where they do not hold.
func resolveSession(procs []SessProc, leaders []Leader, rebooted bool, seen map[int]string) sessResolution {
	var res sessResolution
	byPID := make(map[int]SessProc, len(procs))
	bySID := map[int][]SessProc{}
	for _, p := range procs {
		byPID[p.PID] = p
		bySID[p.SID] = append(bySID[p.SID], p)
	}
	// one entry per session number: a number can be recorded twice (two turns whose leaders got the same pid)
	type slot struct {
		stamps  []string
		leaders []Leader
		min     int64
	}
	slots := map[int]*slot{}
	for _, l := range leaders {
		if l.Empty {
			continue // its session was seen gone: the number is no longer this unit's (a retired leader's number may be anyone's)
		}
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
		s.leaders = append(s.leaders, l)
		if us < s.min {
			s.min = us
		}
	}
	descends := func(p SessProc, root int) bool { // p is root or has root in its parent chain
		for hops := 0; hops < len(procs)+1; hops++ {
			if p.PID == root {
				return true
			}
			q, ok := byPID[p.PPID]
			if !ok || p.PPID <= 1 {
				return false
			}
			p = q
		}
		return false
	}
	member := map[int]SessProc{}
	unproven := map[int]SessProc{}
	var whys []string
	var pids []int
	for pid := range slots {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	for _, pid := range pids {
		s := slots[pid]
		holder, held := byPID[pid]
		alive := false
		if held {
			for _, st := range s.stamps {
				alive = alive || holder.Stamp == st
			}
		}
		inSession := bySID[pid]
		if alive {
			res.live = append(res.live, pid)
			if holder.SID != pid {
				unproven[pid] = holder
				whys = append(whys, fmt.Sprintf("the recorded leader %d does not lead its session", pid))
				continue
			}
		} else if len(inSession) == 0 {
			res.gone = append(res.gone, s.leaders...)
			continue // the leader and its whole session are gone
		} else if rebooted {
			continue // another boot's processes carry the number: not this unit's
		}
		for _, p := range inSession {
			us, ok := parseSessStamp(p.Stamp)
			switch {
			case !ok || us < s.min:
				unproven[p.PID] = p
				whys = append(whys, fmt.Sprintf("process %d in session %d is older than the unit's leader", p.PID, pid))
			case !alive && held && holder.SID == pid && descends(p, pid):
				// the new holder's own session: not ours
			case !alive && held && holder.SID == pid && us >= mustParse(holder.Stamp):
				unproven[p.PID] = p
				whys = append(whys, fmt.Sprintf("session number %d was reused by another process and process %d cannot be told from its", pid, p.PID))
			default:
				member[p.PID] = p
			}
		}
	}
	for _, p := range procs {
		if st, ok := seen[p.PID]; ok && st == p.Stamp {
			member[p.PID] = p
			delete(unproven, p.PID)
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
			if _, already := member[k.PID]; !already {
				member[k.PID] = k
				delete(unproven, k.PID)
				queue = append(queue, k.PID)
			}
		}
	}
	for _, p := range member {
		delete(unproven, p.PID)
		res.members = append(res.members, p)
	}
	sort.Slice(res.members, func(i, j int) bool { return res.members[i].PID < res.members[j].PID })
	for _, p := range unproven {
		res.unproven = append(res.unproven, p)
	}
	sort.Slice(res.unproven, func(i, j int) bool { return res.unproven[i].PID < res.unproven[j].PID })
	if len(whys) > 0 {
		sort.Strings(whys)
		res.unprovenWhy = "not provably this unit's: " + whys[0]
		if len(whys) > 1 {
			res.unprovenWhy += fmt.Sprintf(" (and %d more)", len(whys)-1)
		}
	}
	return res
}

// mustParse is the start time of a process the scan listed (a stamp that does not parse sorts first).
func mustParse(stamp string) int64 {
	us, _ := parseSessStamp(stamp)
	return us
}

// errSessGone is what Info answers for a process that is absent or only a zombie: the one error that means "not there".
// Any other Info error means the read failed, which is not evidence that the process is gone.
var errSessGone = errors.New("no such process")

// stampReader is the optional plain read of a process's start stamp (no consistency bracket): the identity of a member
// when Info cannot give a consistent read.
type stampReader interface {
	StampOf(pid int) (string, error)
}

var errNoSessionOS = errors.New("the session mechanism is only available on macOS")
var errNoLedger = errors.New("no leader ledger is wired, so the unit's sessions are unknown")

// sessView is one read of the world for one unit: the scan, the ledger and the resolution of the two.
type sessView struct {
	all      []SessProc
	rebooted bool
	sessResolution
}

func (c sessContainment) view(rec UnitRecord) (sessView, error) { return c.viewSeen(rec, nil) }

// viewSeen is view with the members an earlier read of the same end already saw (sessGroup.known).
func (c sessContainment) viewSeen(rec UnitRecord, seen map[int]string) (sessView, error) {
	if c.os == nil {
		return sessView{}, errNoSessionOS
	}
	if c.led == nil {
		return sessView{}, errNoLedger
	}
	all, err := c.led.Leaders(rec.Generation)
	if err != nil {
		return sessView{}, fmt.Errorf("the leader ledger cannot be read: %w", err)
	}
	var leaders []Leader
	for _, l := range all {
		if l.Unit == rec.CgroupID { // a leader of another unit of the generation is not this unit's
			leaders = append(leaders, l)
		}
	}
	procs, err := c.os.Scan()
	if err != nil {
		return sessView{}, fmt.Errorf("the processes cannot be listed: %w", err)
	}
	boot, err := c.os.BootID()
	if err != nil {
		return sessView{}, fmt.Errorf("the boot id is unreadable: %w", err)
	}
	rebooted := boot != rec.BootID
	return sessView{all: procs, rebooted: rebooted, sessResolution: resolveSession(procs, leaders, rebooted, seen)}, nil
}
