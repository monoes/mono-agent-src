package coderhost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier/sdconfine"
)

// What the host's constructor (internal/cli/chat) needs to turn the goal checks on from the environment. It lives here so that
// the verifier library and its confiner have exactly one importer outside themselves.

// VerifierInfo is what the host publishes about the checks (host.json, display only).
type VerifierInfo struct {
	Pin         string // the checker's runtime/model, as asked for
	Enabled     bool   // the checks are set up
	Confinement string // plain words: what confines the programs of command criteria here, or what is missing
	Reason      string // why a checker that was asked for is not set up
}

// NewVerifierDeps builds the checks' deps from the flag's pin, the scratch folder and the folders no check may use. It never
// fails the host: a pin or a scratch folder that does not work gives nil deps and the reason. A confiner that cannot be built is
// either absent (macOS, Windows, no systemd user session: command criteria are not run, unless the person allowed unconfined
// checks for a mission, where the library can) or BROKEN (a user manager that cannot fence: a refusal that nothing, including the
// person's switch, turns into an unconfined run).
func NewVerifierDeps(ctx context.Context, pin, scratch string, forbidden []string) (*VerifierDeps, VerifierInfo) {
	pin = strings.TrimSpace(pin)
	info := VerifierInfo{Pin: pin}
	if rt, md, ok := strings.Cut(pin, "/"); !ok || rt == "" || md == "" || strings.ContainsAny(pin, " \t\r\n") {
		info.Reason = fmt.Sprintf("the goal checker pin %q is not runtime/model[@version]; the goal checks stay off", pin)
		return nil, info
	}
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		info.Reason = fmt.Sprintf("the goal checker's scratch folder cannot be made (%v); the goal checks stay off", err)
		return nil, info
	}
	if err := os.Chmod(scratch, 0o700); err != nil {
		info.Reason = fmt.Sprintf("the goal checker's scratch folder cannot be made private (%v); the goal checks stay off", err)
		return nil, info
	}
	d := &VerifierDeps{Pin: pin, Scratch: scratch, Forbidden: forbidden}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c, err := sdconfine.New(cctx, sdconfine.Config{ScratchBase: scratch})
	switch {
	case err == nil:
		d.Confiner, info.Confinement = c, "systemd-run (a unit of your user manager per command, no network, a private copy of the project)"
	case sdconfine.IsUnavailable(err) && !fenceBroken(err):
		info.Confinement = "none: " + err.Error()
	default:
		d.Confiner, info.Confinement = brokenConfiner{err: err}, "BROKEN, project commands are refused: "+err.Error()
	}
	info.Enabled = true
	return d, info
}

// fenceBroken says whether a refusal means a user manager exists that cannot fence (an old version, a property it does not know,
// a controller that is not delegated) as opposed to no user manager or no platform support at all.
func fenceBroken(err error) bool {
	u, ok := err.(*sdconfine.Unavailable)
	if !ok {
		return true // a typed refusal we cannot read, or any other failure: stay refused
	}
	switch u.Missing {
	case sdconfine.MissingProperty, sdconfine.MissingController, sdconfine.MissingSystemdVersion:
		return true
	}
	return false
}

// brokenConfiner is the confiner of a machine whose fence failed to build: its self test always fails with the reason, so the
// checker ends cannot-check/no-confinement and says why, and the person's unconfined switch does not apply (the library never
// falls back to it when a confiner's self test fails).
type brokenConfiner struct{ err error }

var _ verifier.Confiner = brokenConfiner{}

func (brokenConfiner) Name() string                      { return "unavailable" }
func (b brokenConfiner) SelfTest(context.Context) error  { return b.err }
func (b brokenConfiner) Place(*exec.Cmd) (func(), error) { return nil, b.err }
