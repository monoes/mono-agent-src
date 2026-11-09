package coderhost

// Stubs: filled in by the next commit.

func (h *Host) pursuitWakeCap(capUSD float64, capped bool, err error) (float64, bool, error) {
	return capUSD, capped, err
}

func (s *Store) helperCarveUnspentMicro(m pursuitMission, tree []string) (int64, error) {
	return 0, nil
}

func (h *Host) pursuitPinWhy(m pursuitMission) string { return "" }

func (h *Host) pursuitRefuse(m pursuitMission) func(pin string) bool { return h.Pursuit.Refuse }
