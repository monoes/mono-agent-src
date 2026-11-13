package health

import (
	"context"
	"fmt"
	"strings"
)

// CheckOpenMode is the row that says whether the open-ended mode can run on this machine. It reads, it never starts
// anything: each child is one prerequisite, with the exact command or setting that fixes it. The predicates are the
// mode's own (coderhost.OpenModeUnsupportedHere, procbind.Ready, stop.ReadinessSteps, the settings and run reads the chat
// paths use), gathered by the OpenMode hook; this file only turns the answers into rows.
const (
	CheckOpenMode         = "coder.open_mode"
	FixOpenModeBinding    = "coder.open_mode.binding"
	openModeBindingCmd    = "monoagentcli --profile %s coder set --peercred-binding on"
	openModeVaultCmd      = "monoagentcli --profile %s secret add --kind secret --name scratch-key"
	openModeHostStopCmd   = "monoagentcli --profile %s coder host stop"
	openModeActiveStopCmd = "monoagentcli --profile %s doomsday stop"
)

// OpenModeStep is one read-only containment requirement and what it found ("" Err = met).
type OpenModeStep struct {
	Name string
	Err  string
	Fix  string
}

// OpenModeFacts is what this machine answers for the mode. Empty strings and false mean "fine"/"no" except where named.
type OpenModeFacts struct {
	GOOS            string
	Unsupported     string // coderhost.OpenModeUnsupportedHere: "" when this OS can run it
	BindingReadyErr string // procbind.Ready: "" when the binding service can run here
	BindingOn       bool   // security.peercred_binding in this profile
	BindingErr      string // the setting could not be read
	Steps           []OpenModeStep
	HostRunning     bool   // a coder host holds its lock
	ActiveRun       bool   // this profile has a run that is not quiescent
	ActiveErr       string // the run table could not be read
}

func openModeChecks() []Check {
	return []Check{{ID: CheckOpenMode, Group: GroupCore, Title: "Open-ended mode",
		Features: []string{"open-ended chat mode"}, Run: checkOpenMode}}
}

func checkOpenMode(ctx context.Context, env *Env) Result {
	if env.OpenMode == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	if env.ProfileID == "" {
		return noProfile()
	}
	f := env.OpenMode(ctx)
	p := env.ProfileID
	var rows []Result
	add := func(id, title string, st Status, summary, fix string) *Result {
		r := Result{ID: CheckOpenMode + "." + id, Title: title, Status: st, Summary: summary}
		if fix != "" {
			r.Detail = "fix: " + fix
		}
		rows = append(rows, r)
		return &rows[len(rows)-1]
	}

	if f.Unsupported != "" {
		add("os", "Operating system", StatusWarn, f.Unsupported,
			"nothing to change on this machine: the mode stays hidden and refused here until the macOS fix (#1610) ships")
	} else {
		add("os", "Operating system", StatusOK, "supported ("+f.GOOS+")", "")
	}
	if f.BindingReadyErr != "" {
		add("binding_service", "Binding service", StatusWarn, f.BindingReadyErr, "a kernel or system with the peer-credential query (Linux 6.5 or later, macOS, Windows 10 1809 or later)")
	} else {
		add("binding_service", "Binding service", StatusOK, "the kernel can tell which process connected", "")
	}
	for _, s := range f.Steps {
		if s.Err != "" {
			add("containment_"+rowID(s.Name), "Containment: "+s.Name, StatusWarn, s.Err, s.Fix)
		} else {
			add("containment_"+rowID(s.Name), "Containment: "+s.Name, StatusOK, "met", "")
		}
	}
	switch {
	case f.BindingErr != "":
		add("binding", "Peer-credential binding", StatusWarn, "cannot read the setting: "+f.BindingErr, "")
	case !f.BindingOn:
		add("binding", "Peer-credential binding", StatusWarn, "off (off by default): the mode is not offered while it is off",
			fmt.Sprintf(openModeBindingCmd, p)+" (or Settings, Coder, peer-credential binding)")
	default:
		add("binding", "Peer-credential binding", StatusOK, "on", "")
	}

	vault := checkVault(ctx, env)
	if vault.Status == StatusInfo { // no key yet: a run's start needs one
		vault = Result{Status: StatusWarn, Summary: "no vault key yet: a run cannot start without one"}
		vault.Detail = "fix: " + fmt.Sprintf(openModeVaultCmd, p)
	}
	rows = append(rows, withID(vault, "vault", "Vault key"))

	if f.HostRunning {
		add("host", "Coder host", StatusOK, "running: the run's turns go through it", "")
	} else {
		add("host", "Coder host", StatusInfo, "not running: the first coder turn starts it (see it with `coder host status`)", "")
	}

	rows = append(rows, withID(checkMonoesAccount(ctx, env), "account", "monoes.me sign-in"))

	switch {
	case f.ActiveErr != "":
		add("active_run", "Active run", StatusWarn, "cannot read this profile's runs: "+f.ActiveErr, "")
	case f.ActiveRun:
		add("active_run", "Active run", StatusWarn, "an open-ended run is already active in this profile: only one runs at a time, and it stays active until Stop",
			fmt.Sprintf(openModeActiveStopCmd, p))
	default:
		add("active_run", "Active run", StatusOK, "no run", "")
	}

	// The row is advice: whatever a machine lacks, it never fails the core group (`setup --group core` and the CI
	// doctor smoke test require no failing core row), so a missing capability is a warning with its fix.
	for i := range rows {
		if rows[i].Status == StatusFail {
			rows[i].Status = StatusWarn
		}
	}
	return summarizeOpenMode(rows)
}

// withID makes a row of another check one of this one's.
func withID(r Result, id, title string) Result {
	r.ID, r.Title, r.Children, r.Parent, r.Group = CheckOpenMode+"."+id, title, nil, "", ""
	return r
}

// summarizeOpenMode is the parent: the worst of its rows, with the count of those that need attention.
func summarizeOpenMode(rows []Result) Result {
	res := Result{Status: StatusOK, Children: rows}
	var bad []string
	for _, r := range rows {
		switch r.Status {
		case StatusWarn:
			if res.Status == StatusOK {
				res.Status = StatusWarn
			}
			bad = append(bad, r.Title)
		}
	}
	if len(bad) == 0 {
		res.Summary = "can run on this machine"
		return res
	}
	res.Summary = fmt.Sprintf("%d of %d checks need attention: %s", len(bad), len(rows), strings.Join(bad, ", "))
	return res
}
