package health

import (
	"context"
	"fmt"
)

// CheckTurnsCanStart is the row that says whether the coder host's launcher will accept a plain turn on this profile: which launcher the
// binding setting routes to, and whether an open-ended run requires placed (bound) turns. It reads the open-ended mode's facts (the
// OpenMode hook) and starts nothing; it is advice, never required, and never a failure.
const (
	CheckTurnsCanStart       = "coder.turns_can_start"
	turnsBindingOffCmd       = "monoagentcli --profile %s coder set --peercred-binding off"
	turnsBindingOnCmd        = "monoagentcli --profile %s coder set --peercred-binding on"
	turnsCanStartSetting     = "peer-credential binding"
	turnsLegacyLauncherLabel = "the legacy launcher"
)

func turnsCanStartChecks() []Check {
	return []Check{{ID: CheckTurnsCanStart, Group: GroupCore, Title: "Coder turns can start",
		Features: []string{"coder mode"}, Run: checkTurnsCanStart}}
}

func checkTurnsCanStart(ctx context.Context, env *Env) Result {
	if env.OpenMode == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	if env.ProfileID == "" {
		return noProfile()
	}
	f := env.OpenMode(ctx)
	p := env.ProfileID
	warn := func(summary, fix string) Result {
		r := Result{Status: StatusWarn, Summary: summary}
		if fix != "" {
			r.Detail = "fix: " + fix
		}
		return r
	}
	switch {
	case f.BindingErr != "":
		return warn("cannot read the "+turnsCanStartSetting+" setting, so which launcher a turn goes to is not known: "+f.BindingErr, "")
	case f.BindingOn && f.BindingReadyErr != "":
		return warn(turnsCanStartSetting+" is on but this machine cannot run the binding service ("+f.BindingReadyErr+"): every turn is refused",
			fmt.Sprintf(turnsBindingOffCmd, p))
	case f.BindingOn:
		return Result{Status: StatusOK, Summary: "turns start through the bound launcher (" + turnsCanStartSetting + " on)"}
	case f.ActiveErr != "":
		return warn("cannot read whether an open-ended run is open, which would refuse an unbound turn: "+f.ActiveErr, "")
	case f.ActiveRun:
		return warn("an open-ended run is open and "+turnsCanStartSetting+" is off: a run needs placed (bound) turns, so "+turnsLegacyLauncherLabel+" refuses every turn",
			fmt.Sprintf(openModeActiveStopCmd, p)+" (ends the run), or "+fmt.Sprintf(turnsBindingOnCmd, p))
	}
	return Result{Status: StatusOK, Summary: "plain turns start on " + turnsLegacyLauncherLabel + " (" + turnsCanStartSetting + " off, no open run)"}
}
