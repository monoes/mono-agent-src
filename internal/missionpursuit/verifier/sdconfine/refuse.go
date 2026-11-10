package sdconfine

import (
	"errors"
	"fmt"
)

// Missing names the piece this computer lacks for a confinement. Whatever it is, the verifier keeps "no-confinement": project
// commands are not run, and the person's status line can say what is missing.
type Missing string

const (
	// MissingPlatform: not Linux (macOS and Windows have no confiner; a Seatbelt profile is a later design).
	MissingPlatform Missing = "platform"
	// MissingSystemdRun and MissingSystemctl: no root-owned systemd-run or systemctl in /usr/bin or /bin.
	MissingSystemdRun Missing = "systemd-run"
	MissingSystemctl  Missing = "systemctl"
	// MissingUserManager: no systemd user manager answering on this user's session bus (WSL1, containers, Alpine, an ssh login
	// with no user session), or it refused the probe unit.
	MissingUserManager Missing = "user-manager"
	// MissingController: the manager runs the probe unit but a cgroup controller (cpu, memory, pids) is not delegated to it, so
	// a cap would be silently not applied.
	MissingController Missing = "cgroup-controller"
	// MissingProperty: the manager does not know or accept a property of the design's list (or the builder lost one).
	MissingProperty Missing = "property"
)

// Unavailable is the typed refusal: a confiner cannot be built here, or a command cannot be placed, because of Missing.
type Unavailable struct {
	Missing Missing
	Detail  string
}

func (e *Unavailable) Error() string {
	return fmt.Sprintf("this computer cannot confine project commands: %s is missing: %s", e.Missing, e.Detail)
}

// IsUnavailable says whether err is (or wraps) the typed refusal.
func IsUnavailable(err error) bool {
	var u *Unavailable
	return errors.As(err, &u)
}

func unavailable(m Missing, f string, a ...any) error {
	return &Unavailable{Missing: m, Detail: fmt.Sprintf(f, a...)}
}
