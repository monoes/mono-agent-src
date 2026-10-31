package chat

import (
	"context"
	"database/sql"
	"errors"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
)

// gatedLauncher is the coder host's launcher with the peer-credential gate in front. While the setting
// security.peercred_binding is off it only passes every call on, untouched. While it is on and procbind.Refusal
// has a cause (this build does not issue bound tokens, the machine cannot run the service, or the service is
// not up), every turn start is refused with that cause: never a weaker mode. The setting is read at every start,
// so a hand-edited setting refuses too, and so does one switched on after the host started. A start that is already past
// this check when the setting is switched on goes on as it was (legacy) and the next start is refused; the window is the
// few milliseconds between the read and the launch. Today it cannot be reached: "on" cannot be set while IssuanceReady is
// false (the setter refuses and the gate refuses a hand edit). Before IssuanceReady flips, admission must be synchronized
// with the setting change (the 3c/4 flip checklist in the wiring note).
type gatedLauncher struct {
	inner coderhost.Launcher // the legacy launch
	// bound is the same launcher with peer-credential binding on (execLauncher.bound set); the gate sends a turn there
	// only when the setting is on and procbind.Refusal has no cause. Nil: there is none, so "on" is always refused.
	bound coderhost.Launcher
	db    *sql.DB
	// svc is the binding service of the host (nil: there is none, so it is never running).
	svc bindingService
	// insideTurn says whether the host itself runs inside a coder session's turn (nil: it does not). Every turn it
	// started would then be a descendant of that one, which a bound turn must not be.
	insideTurn func() bool
	// logf receives the class-level reason a service could not be started; the operator is told generically.
	logf func(format string, args ...any)
	// reason says why binding may not run (nil: procbind.Refusal); a seam for tests.
	reason func(serviceRunning bool) string
}

func (g *gatedLauncher) why(running bool) string {
	if g.reason != nil {
		return g.reason(running)
	}
	return procbind.Refusal(running)
}

// bindingService is what the gate asks of the host's binding service.
type bindingService interface {
	Ensure(ctx context.Context, profile string) error
	Running(profile string) bool
}

// bindingRefusal is a turn start refused by the gate.
type bindingRefusal struct{ msg string }

func (e *bindingRefusal) Error() string { return e.msg }

// route says which launcher a turn goes to: the legacy one while the setting is off (byte for byte as before), the bound
// one when it is on and nothing in the way, and an error (nothing starts) otherwise.
func (g *gatedLauncher) route(profile string) (l coderhost.Launcher, bound bool, err error) {
	// The whole settings object is read with the loader every other reader uses (coder status, the setter, the
	// turn itself): if it cannot be read as a whole, whether the setting is on is not known and the turn is refused,
	// even if the one field looks fine. A settings object that is valid JSON with another field of the wrong type is
	// such a case.
	s, err := loadCoderSettings(g.db)
	if err != nil {
		return nil, false, &bindingRefusal{"the turn was not started: the coder settings could not be read, so whether peer-credential binding is on is not known"}
	}
	if !bool(s.PeercredBinding) {
		return g.inner, false, nil // off
	}
	if g.insideTurn != nil && g.insideTurn() {
		return nil, false, &bindingRefusal{"the turn was not started: the coder host runs inside a coder session's turn, so every turn it starts would descend from that one; start the host from a terminal"}
	}
	// The service is asked to start only when everything else allows it: while this build cannot issue bound
	// tokens (or the machine cannot run it) nothing is ever started.
	running := false
	if g.svc != nil && g.why(true) == "" {
		if err := g.svc.Ensure(context.Background(), profile); err == nil {
			running = g.svc.Running(profile)
		} else if g.logf != nil {
			g.logf("coder host: the binding service could not be started: %v", err)
		}
	}
	if why := g.why(running); why != "" {
		return nil, false, &bindingRefusal{"the turn was not started: peer-credential binding is switched on, but " + why + ": switch it off (coder set --peercred-binding off)"}
	}
	if g.bound == nil {
		return nil, false, &bindingRefusal{"the turn was not started: peer-credential binding is switched on, but this host has no bound launcher: switch it off (coder set --peercred-binding off)"}
	}
	return g.bound, true, nil
}

// settingIsOnNow reads the setting again after a legacy turn's process exists. It is a guard against a flip in between,
// not a gate: a settings object that cannot be read right now says nothing about a flip, so the healthy turn is left
// running (the next start reads it again, and refuses if it is unreadable).
func (g *gatedLauncher) settingIsOnNow() bool {
	s, err := loadCoderSettings(g.db)
	if err != nil {
		if g.logf != nil {
			g.logf("coder host: the coder settings could not be read after a turn started; the turn is left running: %v", err)
		}
		return false
	}
	return bool(s.PeercredBinding)
}

// launch routes the turn and starts it. A turn that went to the legacy launcher is checked once more after its process
// exists, when binding can be on at all: if the operator switched the setting on in between, the turn is stopped and
// the start refused, so no turn runs unbound after the setting says bound.
func (g *gatedLauncher) launch(profile string, start func(coderhost.Launcher) (coderhost.Turn, error)) (coderhost.Turn, error) {
	l, bound, err := g.route(profile)
	if err != nil {
		return nil, err
	}
	turn, err := start(l)
	if err != nil || bound || !procbind.IssuanceOn() {
		return turn, err
	}
	if g.settingIsOnNow() {
		turn.Stop()
		go func() { _ = turn.Wait() }()
		return nil, &bindingRefusal{"the turn was not started: peer-credential binding was switched on while it started; send again"}
	}
	return turn, nil
}

func (g *gatedLauncher) Start(profileID, conversationID, turnID, message string, attachments []string, missionToken string) (coderhost.Turn, error) {
	return g.launch(profileID, func(l coderhost.Launcher) (coderhost.Turn, error) {
		return l.Start(profileID, conversationID, turnID, message, attachments, missionToken)
	})
}

// StartCapped is Start under a spend cap; the launcher must offer it (the host asks for it by interface).
func (g *gatedLauncher) StartCapped(profileID, conversationID, turnID, message string, attachments []string, missionToken string, capUSD float64) (coderhost.Turn, error) {
	return g.launch(profileID, func(l coderhost.Launcher) (coderhost.Turn, error) {
		b, ok := l.(coderhost.BudgetLauncher)
		if !ok {
			return nil, errors.New("this launcher cannot run a turn under a spend cap")
		}
		return b.StartCapped(profileID, conversationID, turnID, message, attachments, missionToken, capUSD)
	})
}

func (g *gatedLauncher) Adopt(pid int, stamp string) coderhost.Turn { return g.inner.Adopt(pid, stamp) }

func (g *gatedLauncher) Finish(profileID, conversationID, turnID, reason string) {
	g.inner.Finish(profileID, conversationID, turnID, reason)
}
