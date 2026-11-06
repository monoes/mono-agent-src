package main

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/account/accounttest"
)

// The start-time self-tests (stop.ProbeAt: a lock, a transient unix-socket listener, a scratch pinned database) run only from
// the verbs the account gate guards. Signed out, every doomsday verb is either refused by the gate before the engine is built
// or (stop) never probes, so a locked account reaches no self-test listener (internal/accountdoor lists host_probe.go on this
// test). Signed in, the render verb does probe, which shows the counter would have seen a call.
func TestNoDoomsdayVerbRunsTheSelfTestsSignedOutAndRenderDoesSignedIn(t *testing.T) {
	var probes atomic.Int32
	old := probeAt
	probeAt = func(string, string) error { probes.Add(1); return errors.New("self-tests are not run in this test") }
	t.Cleanup(func() { probeAt = old })

	for _, v := range noCharterVerbs(t) {
		t.Run("signed out/"+v.name, func(t *testing.T) {
			noPins(t)
			accounttest.Install(t, accounttest.LockedNoLogin)
			freshHome(t)
			asOperatorProcess(t)
			db := newHILTestDB(t)
			before := probes.Load()
			runMain(t, append([]string{"--db-path", db, "--profile", "default", "--json", "doomsday"}, v.args...)...)
			if n := probes.Load() - before; n != 0 {
				t.Errorf("the self-tests ran %d time(s) for a signed-out %s", n, v.name)
			}
		})
	}
	t.Run("signed in/render (positive control)", func(t *testing.T) {
		countPins(t)
		accounttest.Install(t, accounttest.SignedIn)
		freshHome(t)
		asOperatorProcess(t)
		db := newHILTestDB(t)
		before := probes.Load()
		runMain(t, "--db-path", db, "--profile", "default", "--json", "doomsday", "render")
		if probes.Load() == before {
			t.Error("a signed-in render did not reach the self-tests: the counter proves nothing")
		}
	})
}
