package chat

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/procbind"
)

// gatedLauncher is the coder host's launcher with the peer-credential gate in front. While the setting
// security.peercred_binding is off it only passes every call on, untouched. While it is on and procbind.Refusal
// has a cause (the machine cannot run the service, or the service is not up), every turn start is refused with that
// cause: never a weaker mode. The setting is read at every start, so a hand-edited setting refuses too, and so does one
// switched on after the host started. A start that is already past this check when the setting is switched on is checked
// once more after its process exists and, if the setting is on by then, stopped and refused (the setter also refuses to
// switch it on while a turn runs). A setting that cannot be read at that second look says nothing about a flip and leaves
// the healthy turn running: the first read of the same start already said it was off.
type gatedLauncher struct {
	inner coderhost.Launcher // the legacy launch
	// bound is the same launcher with peer-credential binding on (execLauncher.bound set); the gate sends a turn there
	// only when the setting is on and procbind.Refusal has no cause. Nil: there is none, so "on" is always refused.
	bound coderhost.Launcher
	db    *sql.DB
	// svc is the binding service of the host (nil: there is none, so it is never running).
	svc bindingService
	// insideTurn says whether the host itself runs inside a coder session's turn (nil: it does not). Every turn it
	// started would then be a descendant of that one, which a bound turn must not be. An error means it could not be
	// told, and the gate refuses as if the host were inside a turn.
	insideTurn func() (bool, error)
	// logf receives the class-level reason a service could not be started; the operator is told generically.
	logf func(format string, args ...any)
	// load reads the coder settings (nil: loadCoderSettings); a seam for tests.
	load func(*sql.DB) (coderSettings, error)
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

// tokensLiveRefusal is a refusal after which a token of the refused turn may still be live (it could not be ended). It is a
// bindingRefusal for every caller that asks, and says so to the host, which holds the session.
type tokensLiveRefusal struct {
	ref  *bindingRefusal
	live error
}

func (e *tokensLiveRefusal) Error() string   { return e.ref.Error() + ": " + e.live.Error() }
func (e *tokensLiveRefusal) Unwrap() []error { return []error{e.ref, e.live} }

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
	// Unset is the default: on where this machine can run the service. There, and only there, a failure to bring binding up
	// (service did not start, host inside a turn, no bound launcher) degrades to the legacy launcher with a logged reason
	// instead of refusing, so a default can never lock a machine out of its own coder host. An explicit "on" still refuses,
	// and so does a default while an open-ended run is open (a run needs bound turns).
	explicit := s.PeercredBinding != bindingUnset
	if !explicit {
		if why := g.why(true); why != "" {
			// The machine cannot run it: off, as before; `coder status` says why. But never under a run that started BOUND (a flapping
			// machine check must not take its turns out of binding): only a run recorded as unbound at its start, or no run, goes on.
			n, err := coderhost.BoundRunsOpen(context.Background(), g.db)
			if err != nil {
				return nil, false, &bindingRefusal{"the turn was not started: whether an open-ended run that needs bound turns is open could not be read: " + err.Error()}
			}
			if n > 0 {
				return nil, false, &bindingRefusal{"the turn was not started: an open-ended run that started with peer-credential binding is open, and the binding cannot be used now (" + why + "); no run without binding; `doomsday stop` ends the run if the machine check keeps failing"}
			}
			return g.inner, false, nil
		}
	} else if s.PeercredBinding != bindingOn {
		return g.inner, false, nil // explicitly off
	}
	refuse := func(msg string) (coderhost.Launcher, bool, error) {
		if !explicit {
			runs, err := coderhost.OpenRunCount(context.Background(), g.db)
			if err != nil { // fail closed, and say why: whether an open-ended run needs bound turns could not be read
				return nil, false, &bindingRefusal{msg + " (and whether an open-ended run is open could not be read, so the turn is not run without binding: " + err.Error() + ")"}
			}
			if runs == 0 {
				if g.logf != nil {
					g.logf("coder host: peer-credential binding is on by default but could not be used (%s): this turn runs without it", msg)
				}
				return g.inner, false, nil
			}
		}
		return nil, false, &bindingRefusal{msg + " (an open-ended run is open and needs bound turns, so this turn is not run without binding; `doomsday stop` ends the run if this keeps failing)"}
	}
	if g.insideTurn != nil {
		inside, err := g.insideTurn()
		if err != nil {
			return refuse("the turn was not started: whether the coder host runs inside a coder session's turn could not be determined")
		}
		if inside {
			return refuse("the turn was not started: the coder host runs inside a coder session's turn, so every turn it starts would descend from that one; start the host from a terminal")
		}
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
		return refuse("the turn was not started: peer-credential binding is switched on, but " + why + ": switch it off (coder set --peercred-binding off)")
	}
	if g.bound == nil {
		return refuse("the turn was not started: peer-credential binding is switched on, but this host has no bound launcher: switch it off (coder set --peercred-binding off)")
	}
	return g.bound, true, nil
}

// settingIsOnNow reads the setting again after a legacy turn's process exists. It is a guard against a flip in between,
// not a gate, and it FAILS OPEN: a read that fails is tried again a few times, and one that keeps failing (a busy or locked
// database) is taken as "off" for this turn, because the read that routed the very start already said so and nothing known
// says it was switched on since. The healthy turn keeps running; the next start reads the setting again and refuses if it
// is on or unreadable. A read that works and says "on" stops the turn and refuses the start.
func (g *gatedLauncher) settingIsOnNow() bool {
	load := g.load
	if load == nil {
		load = loadCoderSettings
	}
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var s coderSettings
		if s, err = load(g.db); err == nil {
			return s.PeercredBinding == bindingOn // only an explicit "on" flips under a legacy turn; the default is read once per start
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 25 * time.Millisecond)
		}
	}
	// Persistently unreadable (a busy or locked database): the read that routed this very start said the setting was off, so nothing
	// known says it was switched on since. The healthy turn is left running (this runs after EVERY legacy start, for users who never
	// enabled binding); the next start reads the setting again and refuses if it is on or unreadable.
	if g.logf != nil {
		g.logf("coder host: the coder settings could not be read after a turn started; binding is assumed off for this turn and it is left running: %v", err)
	}
	return false
}

// refusedTurnWait is how long a refused turn gets to exit after its stop (the stop itself allows hostTurnGrace and then
// kills the group).
var refusedTurnWait = hostTurnGrace + 5*time.Second

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
		// The refused turn is stopped, its own tokens ended, and it is waited for (bounded) before the refusal is returned:
		// the host settles the session on the returned error and may start another turn at once. Past the bound the old
		// process may still be running; its late exit then revokes only its own turn's tokens (Refuse scopes it), so it
		// cannot take the next turn's with it.
		var refuseErr error
		if r, ok := turn.(interface{ Refuse() error }); ok {
			if refuseErr = r.Refuse(); refuseErr != nil && g.logf != nil {
				g.logf("coder host: ending the tokens of the refused turn failed: %v", refuseErr)
			}
		} else {
			turn.Stop()
		}
		exited := make(chan struct{})
		var waitErr error
		go func() {
			waitErr = turn.Wait()
			close(exited)
		}()
		var live []error
		if refuseErr != nil {
			live = append(live, &coderhost.TokensLiveError{Err: refuseErr})
		}
		select {
		case <-exited:
			if waitErr != nil {
				if g.logf != nil {
					g.logf("coder host: the refused turn ended with an error: %v", waitErr)
				}
				if coderhost.TokensMayBeLive(waitErr) { // its cleanup could not end the tokens
					live = append(live, waitErr)
				}
			}
		case <-time.After(refusedTurnWait):
			if g.logf != nil {
				g.logf("coder host: the refused turn is still running after %v", refusedTurnWait)
			}
			// it may still be running and its cleanup has not finished: the host holds the session until the exit channel closes
			live = append(live, &coderhost.TokensLiveError{Err: errors.New("the refused turn had not ended"), Ended: exited})
		}
		ref := &bindingRefusal{"the turn was not started: peer-credential binding was switched on while it started; send again"}
		if len(live) > 0 { // a token of the refused turn may still be live: the host must hold the session, not settle it
			return nil, &coderhost.StartedError{Err: &tokensLiveRefusal{ref: ref, live: errors.Join(live...)}}
		}
		return nil, &coderhost.StartedError{Err: ref} // the refused turn's process was created and ended: its cap stays
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
