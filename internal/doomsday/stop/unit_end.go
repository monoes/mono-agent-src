package stop

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Default escalation bounds of note 13.2 section 7; starting values, to be measured.
const (
	DefaultTTerm  = 5 * time.Second
	DefaultTKill  = 5 * time.Second
	defaultPoll   = 50 * time.Millisecond
	maxRmdirTries = 5
)

// EndConfig parameterises EndUnit.
type EndConfig struct {
	Clock Clock
	TTerm time.Duration
	TKill time.Duration
	Poll  time.Duration
	// Exclude names the ticket sub-cgroups (t-<ticket>, and t-<ticket>-<random> as Placer.PlaceTicket names them) of a CONTINUING begun operation that are not touched.
	Exclude map[string]bool
	// Known reports whether the registry has a row for the member; a member without one is "unknown" (it is
	// still ended, inside a unit proven ours, but the outcome is ENDED-WITH-UNKNOWN).
	Known func(sub string, pid int, stamp string) bool
	// StopService is the convenience stop of the systemd service; it is never the proof.
	StopService func() error
}

func (c EndConfig) norm() EndConfig {
	if c.Clock == nil {
		c.Clock = SystemClock{} // EndUnit reads the clock whenever a ticket sub-cgroup exists: never nil
	}
	if c.TTerm == 0 {
		c.TTerm = DefaultTTerm
	}
	if c.TKill == 0 {
		c.TKill = DefaultTKill
	}
	if c.Poll == 0 {
		c.Poll = defaultPoll
	}
	return c
}

// waitEmpty polls "populated 0" until the deadline from the injected clock.
func waitEmpty(cg Cgroup, sub string, clk Clock, d, poll time.Duration) bool {
	end := clk.Now().Add(d)
	for {
		if p, err := cg.Populated(sub); err == nil && !p {
			return true
		}
		if !clk.Now().Before(end) {
			return false
		}
		<-clk.After(poll)
	}
}

// EndUnit ends every sub-cgroup of a unit whose identity was proven (the Cgroup comes from ProveUnit):
// enumerate, SIGTERM through the descriptor, wait, cgroup.kill, wait, rmdir (a successful rmdir is the proof),
// retry while something is alive, list stragglers, list unknown members. A process outside the unit is never
// signalled because every operation is relative to the descriptor.
func EndUnit(cg Cgroup, cfg EndConfig) UnitOutcome {
	cfg = cfg.norm()
	if f, ok := cg.(interface{ Flat() bool }); ok && f.Flat() && len(cfg.Exclude) > 0 {
		// A group without sub-units cannot spare the commands of a begun operation: it is left alone until the
		// operation finishes (the Stop stays pending), never ended around it.
		return UnitOutcome{Kind: UnitEnded, Note: "a begun operation is finishing in a unit that cannot spare it; the unit was not ended"}
	}
	subs, err := cg.Subgroups()
	if err != nil {
		return UnitOutcome{Kind: UnitNotProven, Note: fmt.Sprintf("the unit cannot be enumerated: %v", err)}
	}
	sort.Strings(subs)
	var out UnitOutcome
	out.Kind = UnitEnded
	for _, sub := range subs {
		if excludedSub(cfg.Exclude, sub) {
			continue
		}
		unknown := endSub(cg, sub, cfg, &out)
		out.Unknown = append(out.Unknown, unknown...)
	}
	switch {
	case len(out.Survivors) > 0:
		out.Kind = UnitStragglers
	case len(out.Unknown) > 0:
		out.Kind = UnitEndedWithUnknown
	}
	if out.Kind == UnitEnded && len(cfg.Exclude) == 0 && cfg.StopService != nil {
		_ = cfg.StopService()
	}
	return out
}

func endSub(cg Cgroup, sub string, cfg EndConfig, out *UnitOutcome) (unknown []Survivor) {
	seen := map[int]bool{}
	end := cfg.Clock.Now().Add(cfg.TTerm)
	for {
		pids, err := cg.Procs(sub)
		fresh := false
		if err == nil {
			for _, pid := range pids {
				if seen[pid] {
					continue
				}
				seen[pid] = true
				fresh = true
				stamp, comm, ierr := cg.ProcInfo(sub, pid)
				if ierr != nil || cfg.Known == nil || !cfg.Known(sub, pid, stamp) {
					why := "no registry row"
					if ierr != nil {
						why = "identity unreadable"
					}
					unknown = append(unknown, Survivor{PID: pid, Stamp: stamp, Comm: comm, Sub: sub, Why: why})
				}
				_ = cg.Signal(sub, pid, syscall.SIGTERM)
			}
		}
		// processes fork while we look: repeat until a pass finds nothing new or T_term passes
		if !fresh || !cfg.Clock.Now().Before(end) {
			break
		}
	}
	// rmdir only succeeds on an empty cgroup, so a successful rmdir is the proof; while it fails something is
	// alive or a late creator made something: kill the whole sub-tree again, bounded.
	empty := waitEmpty(cg, sub, cfg.Clock, cfg.TTerm, cfg.Poll)
	for attempt := 0; attempt < maxRmdirTries; attempt++ {
		if empty && cg.Rmdir(sub) == nil {
			return unknown
		}
		_ = cg.Kill(sub)
		empty = waitEmpty(cg, sub, cfg.Clock, cfg.TKill, cfg.Poll)
	}
	pids, _ := cg.Procs(sub)
	for _, pid := range pids {
		stamp, comm, _ := cg.ProcInfo(sub, pid)
		out.Survivors = append(out.Survivors, Survivor{PID: pid, Stamp: stamp, Comm: comm, Sub: sub, Why: "did not die after the kill window"})
	}
	if len(pids) == 0 {
		out.Survivors = append(out.Survivors, Survivor{Sub: sub, Why: "the sub-cgroup could not be removed"})
	}
	return unknown
}

// UnitEnder implements Ender over the identity proof. Records come from the witness (authenticated) and the
// run directory file (key-free); both are proven and both are ended; a disagreement is reported.
type UnitEnder struct {
	K       Kernel
	Dir     RunDir
	Witness func(gen uint64) []UnitRecord
	Cfg     EndConfig
	// StopUnit stops the run unit's service once every member of the unit is ended (UnitCreator's Host). Without
	// it the anchor and the unit's cgroup stay. It is never the proof: after it the cgroup must be absent.
	StopUnit func(ctx context.Context, name string) error
}

// End proves each candidate unit and ends it. A unit that is present on the machine for this generation but
// has no durable binding is NOT signalled: a name pattern and an owner UID are not authority (note 13.2 P1);
// it is reported and the run stays cleanup-pending.
func (u UnitEnder) End(ctx context.Context, gen uint64, exclude []uint64) UnitOutcome {
	var cands []UnitRecord
	if u.Witness != nil {
		cands = append(cands, u.Witness(gen)...)
	}
	rec, ok, err := u.Dir.ReadUnitFile(gen)
	if err != nil {
		return UnitOutcome{Kind: UnitNotProven, Note: err.Error()}
	}
	// A record is authority for the generation it names and no other: a record of another generation (a copied
	// or restored run directory) is never signalled through.
	for _, c := range append(append([]UnitRecord(nil), cands...), rec) {
		if (c != UnitRecord{}) && c.Generation != gen {
			return UnitOutcome{Kind: UnitNotProven, Note: fmt.Sprintf("a unit record names generation %d, not %d", c.Generation, gen)}
		}
	}
	if ok {
		dup := false
		for _, c := range cands {
			dup = dup || c == rec
		}
		if !dup {
			cands = append(cands, rec)
		}
	}
	cfg := u.Cfg
	cfg.Exclude = map[string]bool{}
	for _, id := range exclude {
		cfg.Exclude[fmt.Sprintf("t-%d", id)] = true
	}
	res := UnitOutcome{Kind: UnitNone}
	var notes []string
	if len(cands) == 2 && cands[0] != cands[1] {
		notes = append(notes, "the witness and the run directory file disagree; both named units are ended")
	}
	for _, rec := range cands {
		o := u.endOne(ctx, rec, cfg)
		res = mergeOutcome(res, o)
		if o.Note != "" {
			notes = append(notes, o.Note)
		}
	}
	if un := u.unbound(gen, cands); len(un) > 0 {
		res = mergeOutcome(res, UnitOutcome{Kind: UnitNotProven})
		notes = append(notes, "unit present with no durable binding, not signalled: "+strings.Join(un, ", "))
	}
	res.Note = strings.Join(notes, "; ")
	return res
}

func (u UnitEnder) endOne(ctx context.Context, rec UnitRecord, cfg EndConfig) UnitOutcome {
	p, err := ProveUnit(u.K, rec)
	if err != nil {
		return UnitOutcome{Kind: UnitNotProven, Note: err.Error()}
	}
	if p.Gone {
		if p.BestEffort {
			return UnitOutcome{Kind: UnitBestEffort, Note: p.Note}
		}
		return UnitOutcome{Kind: UnitEnded, Note: p.Note}
	}
	out := EndUnit(p.Cg, cfg)
	p.Cg.Close() // before the absence check: an open handle keeps a Job Object alive
	if out.Kind == UnitEnded && len(cfg.Exclude) == 0 && u.StopUnit != nil {
		out = u.stopService(ctx, rec, out)
	}
	if p.BestEffort && out.Kind == UnitEnded {
		out.Kind = UnitBestEffort
	}
	return out
}

// stopService ends the anchor and removes the unit's cgroup through the manager, then checks that the cgroup is
// really absent: a unit that is still there is reported as a straggler, never as ended.
func (u UnitEnder) stopService(ctx context.Context, rec UnitRecord, out UnitOutcome) UnitOutcome {
	left := func(why string) UnitOutcome {
		out.Kind = UnitStragglers
		out.Survivors = append(out.Survivors, Survivor{PID: rec.AnchorPID, Stamp: rec.AnchorStamp, Comm: "anchor", Why: why})
		return out
	}
	if err := u.StopUnit(ctx, rec.Name); err != nil {
		return left("the unit's service could not be stopped: " + err.Error())
	}
	mk, ok := containments[rec.Kind]
	if !ok {
		return left("the unit's kind is unknown, so its absence cannot be checked: " + rec.Kind)
	}
	what := "cgroup"
	if rec.Kind != KindCgroup2 {
		what = "group"
	}
	present, err := mk(u.K, nil).Present(rec)
	switch {
	case err != nil:
		return left("the unit's " + what + " cannot be checked after its service was stopped: " + err.Error())
	case present:
		return left("the unit's " + what + " is still present after its service was stopped")
	}
	return out
}

// unbound lists the units of this generation visible on the machine that no record binds.
func (u UnitEnder) unbound(gen uint64, bound []UnitRecord) []string {
	root, err := u.K.DelegatedRoot()
	if err != nil {
		return nil
	}
	names, err := u.K.ListUnitNames(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range names {
		if g, _, ok := UnitGeneration(n); !ok || g != gen {
			continue
		}
		known := false
		for _, b := range bound {
			known = known || b.Name == n
		}
		if !known {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// UnboundUnits lists every run unit on the machine that no record binds (used by recovery to report): never a
// signalling decision.
func UnboundUnits(k Kernel, bound []UnitRecord) []string {
	root, err := k.DelegatedRoot()
	if err != nil {
		return nil
	}
	names, _ := k.ListUnitNames(root)
	var out []string
	for _, n := range names {
		if _, _, ok := UnitGeneration(n); !ok {
			continue
		}
		known := false
		for _, b := range bound {
			known = known || b.Name == n
		}
		if !known {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func mergeOutcome(a, b UnitOutcome) UnitOutcome {
	rank := func(k UnitOutcomeKind) int {
		switch k {
		case UnitNotProven:
			return 5
		case UnitStragglers:
			return 4
		case UnitEndedWithUnknown:
			return 3
		case UnitBestEffort:
			return 2
		case UnitEnded:
			return 1
		}
		return 0
	}
	out := a
	if rank(b.Kind) > rank(a.Kind) {
		out.Kind = b.Kind
	}
	out.Survivors = append(out.Survivors, b.Survivors...)
	out.Unknown = append(out.Unknown, b.Unknown...)
	return out
}
