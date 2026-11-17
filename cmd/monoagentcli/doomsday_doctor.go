package main

import (
	"database/sql"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/health"
)

// openModeDoctorExtra is what the doctor's "Open-ended mode" row reads from the mode itself: the containment unit's
// read-only requirements (the probe's own, no unit is started), whether the profile has a run that is not quiescent, and
// whether that run's unit can still take a turn from the running host.
// It lives here because the doctor is agent-reachable and must not name the mode's packages or tables.
func openModeDoctorExtra(cfg *globalConfig) func(*sql.DB, string, int) ([]health.OpenModeStep, bool, string, string) {
	return func(db *sql.DB, profile string, hostPID int) ([]health.OpenModeStep, bool, string, string) {
		return openModeDoctorRead(cfg, db, profile, hostPID)
	}
}

func openModeDoctorRead(cfg *globalConfig, db *sql.DB, profile string, hostPID int) (steps []health.OpenModeStep, activeRun bool, activeErr, stranded string) {
	for _, s := range stop.ReadinessSteps() {
		st := health.OpenModeStep{Name: s.Name, Fix: s.Fix}
		if err := s.Check(); err != nil {
			st.Err = err.Error()
		}
		steps = append(steps, st)
	}
	if db == nil {
		return steps, false, "", ""
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_runs WHERE profile = ? AND state <> 'quiescent'`, profile).Scan(&n); err != nil {
		if !strings.Contains(err.Error(), "no such table") { // a database from before the tables: no run
			activeErr = err.Error()
		}
	}
	if n > 0 && activeErr == "" {
		stranded = strandedRunUnit(cfg, db, profile, hostPID)
	}
	return steps, n > 0, activeErr, stranded
}

// strandedRunUnit is why the profile's open run (its latest generation that is not quiescent) has a unit the running host
// cannot place a turn in ("" when it can, or when that is not known).
func strandedRunUnit(cfg *globalConfig, db *sql.DB, profile string, hostPID int) string {
	var gen uint64
	if err := db.QueryRow(`SELECT MAX(generation) FROM doomsday_runs WHERE profile = ? AND state <> 'quiescent'`, profile).Scan(&gen); err != nil {
		return ""
	}
	return stop.StrandedUnit(stop.RunDir{Dir: doomsdayRunDirPath(cfg)}, gen, hostPID)
}
