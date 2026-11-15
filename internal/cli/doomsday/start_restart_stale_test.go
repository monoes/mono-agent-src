package doomsday

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// staleWire is a wire whose Binding answers StaleHost until the restart ran, as the CLI's wiring does for an old coder host.
func staleWire(t *testing.T, restart func(context.Context) error) (*wireRig, *storage.Database, *int) {
	t.Helper()
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	current, restarts := false, 0
	w.w.Binding = func(context.Context) (bool, error) {
		if !current {
			return false, StaleHost("no run without a current coder host: the running coder host is older than this program")
		}
		return true, nil
	}
	w.w.StaleHostRestart = func(ctx context.Context) error {
		restarts++
		if err := restart(ctx); err != nil {
			return err
		}
		current = true
		return nil
	}
	e := w.w.Engine()
	w.deps.Engine = func() Engine { return e }
	return w, db, &restarts
}

// `start` on an idle stale host restarts it and goes on to make the run; a host that cannot be restarted (busy, not identifiable,
// the stop failed) refuses before the charter is shown and makes no run. Revert proof: dropping the HostRestarter step from
// newStart turns the first case red (it refuses); calling the restart for any error turns the third red.
func TestStartRestartsAnIdleStaleHostThenStarts(t *testing.T) {
	w, db, restarts := staleWire(t, func(context.Context) error { return nil })
	w.start(t)
	if *restarts != 1 {
		t.Fatalf("restarts=%d", *restarts)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 1 {
		t.Fatalf("%d runs", n)
	}
}

func TestStartRefusesWhenTheStaleHostCannotBeRestarted(t *testing.T) {
	for _, why := range []string{
		"it was not restarted automatically because an open-ended run is open",
		"it was not restarted automatically because it could not be stopped by itself (operation not permitted)",
	} {
		w, db, restarts := staleWire(t, func(context.Context) error { return errors.New(why) })
		h := fullHash(t, w.rig, w.charter)
		out, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h)
		if err == nil || !strings.Contains(err.Error(), why) || *restarts != 1 {
			t.Fatalf("%q: err=%v restarts=%d", why, err, *restarts)
		}
		if strings.Contains(out, "ship it") {
			t.Fatalf("the charter was shown: %q", out)
		}
		if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 0 {
			t.Fatalf("%d run(s) made", n)
		}
	}
}

// A refusal that is not a stale host never restarts anything, and the availability probe only reports a stale host.
func TestOnlyAStaleHostRestartsAndAvailabilityNeverDoes(t *testing.T) {
	w, _, restarts := staleWire(t, func(context.Context) error { return nil })
	w.w.Binding = func(context.Context) (bool, error) { return false, nil } // off: refused for its own reason
	e := w.w.Engine()
	w.deps.Engine = func() Engine { return e }
	h := fullHash(t, w.rig, w.charter)
	if _, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h); err == nil || *restarts != 0 {
		t.Fatalf("binding off: err=%v restarts=%d", err, *restarts)
	}

	w, _, restarts = staleWire(t, func(context.Context) error { return nil })
	res := Availability(context.Background(), w.w.Engine())
	if res.ModeAvailable || !strings.Contains(res.Reason, "older than this program") || *restarts != 0 {
		t.Fatalf("availability: %+v restarts=%d", res, *restarts)
	}
}
