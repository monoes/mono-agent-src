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
