package stop

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
