package doctor

import (
	"context"
	"database/sql"
	"runtime"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/chat"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/procbind"
)

// addOpenModeHook feeds the coder.open_mode row from the mode's own predicates. Every read is local and changes nothing:
// no probe unit, no host step, no network, no process.
func addOpenModeHook(env *health.Env, dbPath string) {
	env.OpenMode = func(context.Context) health.OpenModeFacts {
		return openModeFacts(env.DB, env.ProfileID, dbPath)
	}
}

func openModeFacts(db *sql.DB, profile, dbPath string) health.OpenModeFacts {
	f := health.OpenModeFacts{GOOS: runtime.GOOS, Unsupported: coderhost.OpenModeUnsupportedHere(), HostRunning: chat.HostRunningAt(dbPath)}
	if err := procbind.Ready(); err != nil {
		f.BindingReadyErr = err.Error()
	}
	for _, s := range stop.ReadinessSteps() {
		st := health.OpenModeStep{Name: s.Name, Fix: s.Fix}
		if err := s.Check(); err != nil {
			st.Err = err.Error()
		}
		f.Steps = append(f.Steps, st)
	}
	if db == nil {
		return f
	}
	if on, err := chat.PeercredBindingOn(db); err != nil {
		if !noTable(err) { // a database from before the tables: nothing stored, so the binding is off
			f.BindingErr = err.Error()
		}
	} else {
		f.BindingOn = on
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM doomsday_runs WHERE profile = ? AND state <> 'quiescent'`, profile).Scan(&n); err != nil {
		if !noTable(err) {
			f.ActiveErr = err.Error()
		}
	}
	f.ActiveRun = n > 0
	return f
}

func noTable(err error) bool { return strings.Contains(err.Error(), "no such table") }
