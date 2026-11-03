package doomsday

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// `start` refuses while binding is off, before the charter is shown and with nothing written; unknown and unreadable are
// not "on". Mutants tried: drop the BindingRequirer check in newStart (Enter still refuses, but the charter is shown: the
// output assertion is red); drop RequireBinding in Enter (the direct-Enter assertion is red).
func TestStartRefusesWhileBindingIsOff(t *testing.T) {
	cases := map[string]func(context.Context) (bool, error){
		"off":     func(context.Context) (bool, error) { return false, nil },
		"unknown": nil,
		"error":   func(context.Context) (bool, error) { return true, errors.New("settings locked") },
	}
	for name, fn := range cases {
		w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
		w.w.Binding = fn
		e := w.w.Engine()
		w.deps.Engine = func() Engine { return e }
		h := fullHash(t, w.rig, w.charter)
		out, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h)
		if err == nil || !strings.Contains(err.Error(), "no run without binding") {
			t.Fatalf("%s: start did not refuse for binding: %v", name, err)
		}
		if strings.Contains(out, "ship it") {
			t.Fatalf("%s: the charter was shown for a start that cannot happen: %q", name, out)
		}
		if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 0 {
			t.Fatalf("%s: %d run row(s) written", name, n)
		}
		canon, err := e.Canonical(context.Background(), mustCharter(t, w.charter))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Enter(context.Background(), canon, canon.Hash, Ack{Kind: "script"}); err == nil || !strings.Contains(err.Error(), "no run without binding") {
			t.Fatalf("%s: Enter itself did not refuse: %v", name, err)
		}
	}
}

func mustCharter(t *testing.T, path string) Charter {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := LoadCharter(f)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
