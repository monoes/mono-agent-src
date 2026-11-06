package stop

// ProvenEmpty is true when the recorded unit is PROVEN to hold no process: a reboot, an absent cgroup, or a proven
// cgroup that holds only its anchor. A best-effort answer (a Job Object that is absent or holds only its anchor) is
// never proof, because a process may have left the group: unknown is not gone.
func ProvenEmpty(k Kernel, rec UnitRecord) (bool, error) {
	p, err := ProveUnit(k, rec)
	if err != nil {
		return false, err
	}
	if p.Gone {
		return !p.BestEffort, nil
	}
	defer p.Cg.Close()
	if p.BestEffort {
		return false, nil
	}
	return UnitEmpty(p.Cg, rec)
}

// UnitEmpty reports whether the proven unit holds no member: nothing in any sub-cgroup (the cgroup's populated
// flag covers descendants) and nothing in the unit's own cgroup but the anchor the record names, matched by pid
// and start stamp. Anything it cannot read, and a record without an anchor identity, is "not empty": unknown is
// not gone.
func UnitEmpty(cg Cgroup, rec UnitRecord) (bool, error) {
	pids, err := cg.Procs("")
	if err != nil {
		return false, err
	}
	for _, pid := range pids {
		if rec.AnchorPID == 0 || pid != rec.AnchorPID {
			return false, nil
		}
		stamp, _, err := cg.ProcInfo("", pid)
		if err != nil || stamp != rec.AnchorStamp {
			return false, nil
		}
	}
	subs, err := cg.Subgroups()
	if err != nil {
		return false, err
	}
	for _, sub := range subs {
		if sub == "" {
			continue
		}
		populated, err := cg.Populated(sub)
		if err != nil || populated {
			return false, err
		}
	}
	return true, nil
}
