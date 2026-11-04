package policy

import (
	"math"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// RecoverySamples is the number of consecutive good samples that raise the value by one step. Lowering is immediate;
// raising is slow, so the value does not flap with the load.
const RecoverySamples = 3

// Exit thresholds sit on the safe side of the enter thresholds in the governor package (hysteresis band): a signal
// that entered pressure stays in it until the machine is clearly better, and while in the band the value is held.
const (
	loadExitFactor = 0.7 // enter at governor.LoadFactor (0.8) x cores
	memLowExitFrac = 0.20
	memStopExit    = 0.10
	diskExitBytes  = 12 << 30
	diskExitFrac   = 0.12
)

// state is the hysteresis memory; losing it restarts conservatively at the floor on the next failed reading.
type state struct {
	init     bool
	level    int // the value currently granted
	good     int // consecutive samples that wanted a higher value
	loadHigh bool
	memLow   bool
	memStop  bool
	diskLow  bool
}

// update moves the pressure flags through their enter/exit bands.
func (st *state) update(s governor.Sample) {
	enter := governor.LoadFactor * float64(s.Cores)
	st.loadHigh = band(st.loadHigh, s.Load1 > enter, s.Load1 <= loadExitFactor*float64(s.Cores))
	st.memStop = band(st.memStop, s.MemAvailFrac < governor.MemStopFrac, s.MemAvailFrac >= memStopExit)
	st.memLow = band(st.memLow, s.MemAvailFrac < governor.MemLowFrac, s.MemAvailFrac >= memLowExitFrac)
	diskIn := s.DiskFreeBytes < governor.DiskLowBytes || s.DiskFreeFrac < governor.DiskLowFrac
	diskOut := s.DiskFreeBytes >= diskExitBytes && s.DiskFreeFrac >= diskExitFrac
	st.diskLow = band(st.diskLow, diskIn, diskOut)
}

func band(active, enter, exit bool) bool {
	if enter {
		return true
	}
	if exit {
		return false
	}
	return active
}

// target is what the sample allows right now, before the recovery ramp, with the reasons in a fixed order.
// Never above capN, never below floor. While any flag is active the target is also held at the current level.
func (st *state) target(s governor.Sample, capN, floor int) (int, []governor.Reason) {
	limit := capN
	var reasons []governor.Reason
	if st.loadHigh {
		reasons = append(reasons, governor.ReasonLoad)
		limit = min(limit, loadLimit(s, capN))
	}
	switch {
	case st.memStop:
		reasons = append(reasons, governor.ReasonMemoryStop)
		limit = floor
	case st.memLow:
		reasons = append(reasons, governor.ReasonMemory)
		limit = min(limit, capN/2)
	}
	if st.diskLow {
		reasons = append(reasons, governor.ReasonDisk)
		limit = min(limit, capN/2)
	}
	if len(reasons) > 0 {
		limit = min(limit, st.level)
	}
	return clampInt(limit, floor, capN), reasons
}

// loadLimit scales the cap by how far the load is over the enter threshold.
func loadLimit(s governor.Sample, capN int) int {
	if s.Load1 <= 0 {
		return capN
	}
	v := math.Floor(float64(capN) * governor.LoadFactor * float64(s.Cores) / s.Load1)
	if !(v < float64(capN)) {
		return capN
	}
	if v < 0 {
		return 0
	}
	return int(v)
}
