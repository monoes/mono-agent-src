package procbind

import "fmt"

// MaxHops is how many processes of one caller's ancestry the walk examines, the caller included.
const MaxHops = 64

// Handle names one process incarnation, not a number: on Linux a pidfd. Alive is false once
// the process has exited (reaped or not); every other answer is only meaningful while it is true.
type Handle interface {
	Alive() bool
	// Pid is the process's number in the reader's pid namespace; an error once it is gone.
	Pid() (int, error)
	Close() error
}

// Procs reads the host's process table through handles. Parent and Start read by number (that is
// all /proc offers), so the walk brackets every read with liveness checks of the handle.
type Procs interface {
	// Open pins the process that holds pid now. It fails when there is none.
	Open(pid int) (Handle, error)
	// Parent is h's parent's number, Start the clock tick its process started at.
	Parent(h Handle) (int, error)
	Start(h Handle) (uint64, error)
}

// DenyReason says why the walk refused. It is for logs and tests; a verifier is told only deny.
type DenyReason string

const (
	ReasonNone        DenyReason = ""
	ReasonExited      DenyReason = "a process of the chain exited"
	ReasonUnreadable  DenyReason = "a process of the chain could not be read"
	ReasonReparented  DenyReason = "the chain ends at init or a subreaper without reaching the worker"
	ReasonParentGone  DenyReason = "a parent exited or could not be opened"
	ReasonParentMoved DenyReason = "a process changed parent while it was read"
	ReasonStartOrder  DenyReason = "a parent started after its child"
	ReasonNoMatch     DenyReason = "the worker is not among the caller's ancestors"
)

// Result is the walk's decision.
type Result struct {
	Allowed bool
	Reason  DenyReason
}

func deny(r DenyReason) Result { return Result{Reason: r} }

// Walk decides whether the process worker is the process caller or one of its ancestors, as a
// point-in-time check (section 5 of the design). The caller and worker handles belong to the
// caller of Walk and are not closed; the handles Walk opens for the parents are closed before it
// returns. It fails closed: any process that exits, cannot be read, changes parent or starts
// out of order while it is read is a deny, and so is a chain that does not reach worker.
//
// The only way to allow is to reach the end of the function: success of the search sets matched,
// and the final loop re-checks that every process taken into the walk, and the worker, is still
// alive.
func Walk(p Procs, caller, worker Handle) Result {
	var opened []Handle
	defer func() {
		for _, h := range opened {
			_ = h.Close()
		}
	}()
	handles := []Handle{caller}
	cur := caller
	matched := false
	for hop := 0; hop < MaxHops; hop++ {
		same, r := sameLive(cur, worker)
		if r != ReasonNone {
			return deny(r)
		}
		if same {
			matched = true
			break
		}
		parent, start, r := readStable(p, cur)
		if r != ReasonNone {
			return deny(r)
		}
		if parent <= 1 {
			return deny(ReasonReparented)
		}
		pp, err := p.Open(parent)
		if err != nil {
			return deny(ReasonParentGone)
		}
		opened = append(opened, pp)
		handles = append(handles, pp)
		// the parent is pinned now: read cur's parent again; it must not have moved
		parent2, _, r := readStable(p, cur)
		if r != ReasonNone {
			return deny(r)
		}
		if parent2 != parent {
			return deny(ReasonParentMoved)
		}
		ps, err := p.Start(pp)
		if err != nil {
			return deny(ReasonUnreadable)
		}
		if ps > start {
			return deny(ReasonStartOrder)
		}
		// pp is not checked alive here: if it died during the read the start time may be another
		// process's, which can only make the order check wrong, and the final validation below
		// polls every handle of the chain once the match is complete.
		cur = pp
	}
	if !matched {
		return deny(ReasonNoMatch)
	}
	for _, h := range append(handles, worker) {
		if !h.Alive() {
			return deny(ReasonExited)
		}
	}
	return Result{Allowed: true}
}

// sameLive says whether a and b are one process: both alive before and after their numbers are
// read, and the numbers equal (two live processes never share a number).
func sameLive(a, b Handle) (bool, DenyReason) {
	if !a.Alive() || !b.Alive() {
		return false, ReasonExited
	}
	pa, err := a.Pid()
	if err != nil {
		return false, ReasonUnreadable
	}
	pb, err := b.Pid()
	if err != nil {
		return false, ReasonUnreadable
	}
	if !a.Alive() || !b.Alive() {
		return false, ReasonExited
	}
	return pa == pb, ReasonNone
}

// readStable reads h's parent and start time, which are read by number: h must be alive before
// and after, so the numbers were not read from a process that took h's number.
func readStable(p Procs, h Handle) (parent int, start uint64, r DenyReason) {
	if !h.Alive() {
		return 0, 0, ReasonExited
	}
	parent, err := p.Parent(h)
	if err != nil {
		return 0, 0, ReasonUnreadable
	}
	start, err = p.Start(h)
	if err != nil {
		return 0, 0, ReasonUnreadable
	}
	if !h.Alive() {
		return 0, 0, ReasonExited
	}
	return parent, start, ReasonNone
}

func (r Result) String() string {
	if r.Allowed {
		return "allow"
	}
	return fmt.Sprintf("deny (%s)", string(r.Reason))
}
