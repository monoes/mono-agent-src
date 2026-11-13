package doctor

import (
	"context"
	"database/sql"
	"runtime"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/chat"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/procbind"
)

// addOpenModeHook feeds the coder.open_mode row from the mode's own predicates. Every read is local and changes nothing:
// no probe unit, no host step, no network, no process.
//
// extra supplies what only the operator-side wiring may touch (the containment steps and the run table); nil when absent.
func addOpenModeHook(env *health.Env, dbPath string, extra OpenModeExtra) {
	env.OpenMode = func(context.Context) health.OpenModeFacts {
		return openModeFacts(env.DB, env.ProfileID, dbPath, extra)
	}
}

// OpenModeExtra reads the containment steps and whether the profile has an active run. The CLI provides it from its
// operator-side package: agent-reachable code (this one) names neither the mode's packages nor its tables.
type OpenModeExtra func(db *sql.DB, profile string) (steps []health.OpenModeStep, activeRun bool, activeErr string)

func openModeFacts(db *sql.DB, profile, dbPath string, extra OpenModeExtra) health.OpenModeFacts {
	f := health.OpenModeFacts{GOOS: runtime.GOOS, Unsupported: coderhost.OpenModeUnsupportedHere(), HostRunning: chat.HostRunningAt(dbPath)}
	if err := procbind.Ready(); err != nil {
		f.BindingReadyErr = err.Error()
	}
	if extra != nil {
		f.Steps, f.ActiveRun, f.ActiveErr = extra(db, profile)
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
	return f
}

func noTable(err error) bool { return strings.Contains(err.Error(), "no such table") }
