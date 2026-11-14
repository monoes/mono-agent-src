package main

import (
	"database/sql"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/health"
)

// openModeDoctorExtra is what the doctor's "Open-ended mode" row reads from the mode itself: the containment unit's
// read-only requirements (the probe's own, no unit is started) and whether the profile has a run that is not quiescent.
// It lives here because the doctor is agent-reachable and must not name the mode's packages or tables.
func openModeDoctorExtra(db *sql.DB, profile string) (steps []health.OpenModeStep, activeRun bool, activeErr string) {
	for _, s := range stop.ReadinessSteps() {
		st := health.OpenModeStep{Name: s.Name, Fix: s.Fix}
		if err := s.Check(); err != nil {
			st.Err = err.Error()
		}
		steps = append(steps, st)
	}
	if db == nil {
		return steps, false, ""
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_runs WHERE profile = ? AND state <> 'quiescent'`, profile).Scan(&n); err != nil {
		if !strings.Contains(err.Error(), "no such table") { // a database from before the tables: no run
			activeErr = err.Error()
		}
	}
	return steps, n > 0, activeErr
}
