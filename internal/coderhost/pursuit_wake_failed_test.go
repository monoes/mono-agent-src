package coderhost

import (
	"errors"
	"testing"
	"time"
)

// failWake runs the queued wake-up through the host with a launcher that cannot start the turn, so the command ends failed.
func (w *pworld) failWake(err error) {
	w.t.Helper()
	w.launched().startErr = err
	if terr := w.h.Tick(); terr != nil {
		w.t.Fatal(terr)
	}
	w.launched().startErr = nil
}

func (w *pworld) wakeCounters() (wakesSince, hour int) {
	w.t.Helper()
	st, ok, err := w.st.pursuitStateOf(pursuitSession)
	if err != nil || !ok {
		w.t.Fatalf("pursuit state: %v %v", ok, err)
	}
	hour, _, err = w.st.pursuitWakesLastHour()
	if err != nil {
		w.t.Fatal(err)
	}
	return st.WakesSince, hour
}

// A wake-up whose turn could not start spent nothing: it comes back out of the mission's wake count and the host's hourly count.
// Revert proof: without refundWake and the unstartedFailedSQL exclusion the counters stay at 1.
func TestAWakeUpWhoseTurnCouldNotStartIsNotCountedAsAWakeUp(t *testing.T) {
	w := newPursuitWorld(t)
	w.queueWake()
	if since, hour := w.wakeCounters(); since != 1 || hour != 1 {
		t.Fatalf("setup: a queued wake-up counts once: since=%d hour=%d", since, hour)
	}
	w.failWake(errors.New("placing a turn needs a bound launch"))
	if got := w.wakes(); len(got) != 0 {
		t.Fatalf("the failed wake-up is still pending: %+v", got)
	}
	if since, hour := w.wakeCounters(); since != 0 || hour != 0 {
		t.Fatalf("a wake-up that never started was counted: since=%d hour=%d", since, hour)
	}
}

// A wake-up that did start (and later failed) stays counted: it spent.
func TestAWakeUpThatStartedStaysCounted(t *testing.T) {
	w := newPursuitWorld(t)
	c := w.queueWake()
	w.exec(`UPDATE coder_session_commands SET status = 'failed', error = ? WHERE id = ?`, startedMarker+" but could not record the session", c.ID)
	if since, hour := w.wakeCounters(); since != 1 || hour != 1 {
		t.Fatalf("a started turn's wake-up was given back: since=%d hour=%d", since, hour)
	}
}

// The retry of a failed start waits, doubling, capped; the tries of an epoch stay bounded. Revert proof: without wakeRetryAt the retry is
// queued on the very next pass.
func TestAFailedWakeStartIsRetriedAfterAGrowingWaitAndStaysBounded(t *testing.T) {
	w := newPursuitWorld(t)
	w.queueWake()
	boom := errors.New("launch refused")
	wait := wakeRetryBase
	for try := 1; try < maxWakeTries; try++ {
		w.failWake(boom)
		w.pass()
		if got := w.wakes(); len(got) != 0 {
			t.Fatalf("try %d: the retry was queued at once: %+v", try, got)
		}
		w.advance(wait - time.Second)
		w.pass()
		if got := w.wakes(); len(got) != 0 {
			t.Fatalf("try %d: the retry was queued %v after the failure, before its wait of %v", try, wait-time.Second, wait)
		}
		w.advance(2 * time.Second)
		w.pass()
		if got := w.wakes(); len(got) != 1 {
			t.Fatalf("try %d: the retry was not queued after its wait: %+v", try, got)
		}
		wait = min(wait*2, wakeRetryMax)
	}
	w.failWake(boom)                      // the last try
	w.advance(wakeRetryMax + time.Minute) // well past the wait, before the next epoch is due
	w.pass()
	if got := w.wakes(); len(got) != 0 {
		t.Fatalf("tried again beyond the bound: %+v", got)
	}
	if since, hour := w.wakeCounters(); since != 0 || hour != 0 {
		t.Fatalf("failed starts left a count: since=%d hour=%d", since, hour)
	}
}

// A launch that created the process (a StartedError) may have spent up to the wake cap: its wake-up stays counted, through the host's own
// failure path, and the tries of the epoch stay bounded. Revert proof: dropping the started marker from the launch-error path (host.go),
// or the marker test in refundWake, gives the wake-ups back and the counts stay at zero.
func TestALaunchThatCreatedTheProcessKeepsItsWakeUpCountedWhateverTheNumberOfTries(t *testing.T) {
	w := newPursuitWorld(t)
	w.queueWake()
	boom := &StartedError{Err: errors.New("process created, then refused")}
	for try := 1; try <= maxWakeTries; try++ {
		w.failWake(boom)
		if since, hour := w.wakeCounters(); since != try || hour != try {
			t.Fatalf("try %d: a launch that created the process was given back: since=%d hour=%d", try, since, hour)
		}
		if try < maxWakeTries {
			w.exec(`UPDATE coder_session_turn_caps SET cap_usd = 0`) // a started turn's cap is spent against the lead's small test budget: release it so each try can launch
			w.advance(wakeRetryMax + time.Second)
			w.pass()
			if len(w.wakes()) != 1 {
				t.Fatalf("try %d: no retry queued: %+v", try, w.wakes())
			}
		}
	}
}

// A refund belongs to the generation the wake-up was counted in: when the person acted since (the count was zeroed and counts newer
// wake-ups), an older wake-up that fails to start does not take one back. Revert proof: without the person_at guard the count drops.
func TestAFailedStartDoesNotRefundAWakeUpOfAnOlderGeneration(t *testing.T) {
	w := newPursuitWorld(t)
	w.queueWake()
	after := w.clk.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	w.exec(`UPDATE mission_pursuit_state SET wakes_since = 2, person_at = ? WHERE session_id = ?`, after, pursuitSession)
	w.failWake(errors.New("launch refused before any process"))
	if st, ok, err := w.st.pursuitStateOf(pursuitSession); err != nil || !ok || st.WakesSince != 2 {
		t.Fatalf("an older wake-up took back a newer one's count: %+v %v %v", st, ok, err)
	}
}
