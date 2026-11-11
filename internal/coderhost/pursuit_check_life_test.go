package coderhost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
)

// blocking makes the verifier wait: it says when it started, and ends when released or when its context ends (abandoned).
func (w *cworld) blocking() (started chan struct{}, release chan struct{}) {
	started, release = make(chan struct{}, 4), make(chan struct{})
	var once sync.Once
	w.releases[release] = &once
	w.t.Cleanup(func() { w.releaseCheck(release) }) // a test that does not release it must not hang its cleanup
	w.fn = func(ctx context.Context, req verifier.Request) (verifier.Result, error) {
		started <- struct{}{}
		select {
		case <-release:
			return resultOf(req, verifier.OutcomeFailed, verifier.ReasonCriteriaFailed, 0, true), nil
		case <-ctx.Done():
			return resultOf(req, verifier.OutcomeAbandoned, verifier.ReasonCancelled, 0, false), nil
		}
	}
	return started, release
}

func (w *cworld) releaseCheck(release chan struct{}) {
	w.releases[release].Do(func() { close(release) })
}

// untilStarted passes (without waiting for the check) until the verifier has been called.
func (w *cworld) untilStarted(started chan struct{}) {
	w.t.Helper()
	for i := 0; i < 14; i++ {
		w.pass()
		select {
		case <-started:
			return
		case <-time.After(150 * time.Millisecond):
		}
		w.advance(31 * time.Second)
	}
	w.t.Fatal("the check never started")
}

// Revert proof: run the check inline in the pass (no goroutine) and the pass below never returns while the check is out.
func TestASlowCheckDoesNotHoldThePass(t *testing.T) {
	w := newCW(t)
	started, release := w.blocking()
	w.untilStarted(started)
	done := make(chan struct{})
	go func() { w.pass(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a pass is held by a check that is running")
	}
	if w.h.pursuitBusy.Load() {
		t.Error("the pass flag is held by the check")
	}
	if got := w.waiting(); got != pursuitWaitingCheck {
		t.Errorf("waiting = %q while a check runs, want %q", got, pursuitWaitingCheck)
	}
	if s := w.spend(); s.Micro != missionpursuit.CheckCapMicro {
		t.Errorf("a running check counts at the cap, got %d", s.Micro)
	}
	w.releaseCheck(release)
	w.wait()
	if got := w.checks(); len(got) != 1 || got[0] != "1:done:failed:criteria-failed" {
		t.Errorf("checks = %v", got)
	}
}

// Nothing else spends while a check runs, and the host runs one at a time (a second pass starts no second check).
func TestNothingElseSpendsWhileACheckRuns(t *testing.T) {
	w := newCW(t)
	w.exec(`UPDATE coder_sessions SET unattended = 1 WHERE conversation_id = 'm'`)
	w.exec(`INSERT INTO tasks (profile_id, title, status, position, session_id, kind, created_at, updated_at) VALUES ('p','x','archived',0,77,'task','x','x')`)
	started, release := w.blocking()
	w.untilStarted(started)
	judges, wakes := w.model.count(), w.wakeRows()
	for i := 0; i < 6; i++ {
		w.pass()
		w.advance(31 * time.Minute)
	}
	if w.model.count() != judges || w.wakeRows() != wakes || len(w.requests()) != 1 {
		t.Errorf("judge %d->%d, wakes %d->%d, checks started %d: something spent while a check ran", judges, w.model.count(), wakes, w.wakeRows(), len(w.requests()))
	}
	w.releaseCheck(release)
	w.wait()
}

// Revert proof: drop the watcher and a paused pursuit's check runs on; or record it as free and it counts nothing.
func TestAPausedPursuitCancelsItsCheckAndItCountsAtTheCap(t *testing.T) {
	w := newCW(t)
	started, _ := w.blocking()
	w.untilStarted(started)
	w.pause()
	ended := make(chan struct{})
	go func() { w.wait(); close(ended) }() // the watcher ends the check within a few milliseconds (WatchEvery)
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("a paused pursuit's check was not cancelled within 5 seconds")
	}
	got := w.checks()
	if len(got) != 1 || got[0] != "1:abandoned::cancelled" && got[0] != "1:abandoned::" {
		t.Fatalf("checks = %v, want one abandoned check", got)
	}
	if s := w.spend(); s.Micro != missionpursuit.CheckCapMicro || s.Count != 1 {
		t.Errorf("an abandoned check counts at the cap: %+v", s)
	}
}

func TestTheHostStoppingCancelsACheckAndEndsItsRow(t *testing.T) {
	w := newCW(t)
	started, _ := w.blocking()
	ctx, cancel := context.WithCancel(context.Background())
	for i := 0; i < 14; i++ {
		w.h.pursuitPass(ctx)
		select {
		case <-started:
			i = 99
		case <-time.After(150 * time.Millisecond):
			w.advance(31 * time.Second)
		}
	}
	cancel()
	w.h.pursuitCheckStop()
	if got := w.checks(); len(got) != 1 || got[0][:11] != "1:abandoned" {
		t.Fatalf("checks = %v", got)
	}
	if s := w.spend(); s.Micro != missionpursuit.CheckCapMicro {
		t.Errorf("spend %d", s.Micro)
	}
}

// A check row left running by an earlier host is ended as abandoned at the cap, only when it started before this host did; the
// round goes on from the rows.
func TestARestartEndsTheCheckAnEarlierHostLeftAndTheRoundGoesOn(t *testing.T) {
	w := newCW(t)
	w.pass()
	for i := int64(1); i <= 3; i++ {
		w.rawCheck(i, "failed", "criteria-failed", 0, 1)
	}
	at := w.clk.Now().UTC().Format(time.RFC3339)
	w.exec(`INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, subject_root, started_at, written_by) VALUES (77, ?, 4, 'running', ?, ?, 'host-verifier')`, goalOf(w.pworld).Hash, w.root, at)
	w.advance(time.Minute)
	w.h.pursuitCheckStart(context.Background())
	got := w.checks()
	if len(got) != 4 || got[3] != "4:abandoned::host-restarted" {
		t.Fatalf("checks = %v", got)
	}
	if s := w.spend(); s.Micro != missionpursuit.CheckCapMicro {
		t.Errorf("the swept check counts at the cap: %d", s.Micro)
	}
	w.advance(61 * time.Minute) // the host's hour is not what is tested here
	w.personActs()
	w.untilChecked(5)
	if got := w.checks(); len(got) != 5 || got[4] != "5:done:failed:criteria-failed" {
		t.Fatalf("the next round is MAX(round)+1: %v", got)
	}
}

func TestACheckThatStartedAfterThisHostIsNotSweptFromUnderIt(t *testing.T) {
	w := newCW(t)
	w.pass()
	w.h.pursuitCheckStart(context.Background()) // this host starts now
	w.advance(10 * time.Second)
	at := w.clk.Now().UTC().Format(time.RFC3339)
	w.exec(`INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, subject_root, started_at, written_by) VALUES (77, ?, 1, 'running', ?, ?, 'host-verifier')`, goalOf(w.pworld).Hash, w.root, at)
	if n, err := missionpursuit.NewHostChecks(missionpursuit.NewStore(w.st.db)).AbandonRunning(context.Background(), w.h.hostStart); err != nil || n != 0 {
		t.Fatalf("a host swept a live check of one that started after it: %d %v", n, err)
	}
	if got := w.checks(); len(got) != 1 || got[0] != "1:running::" {
		t.Errorf("checks = %v", got)
	}
}

// The sweep runs whenever the loop is ready, also with the checks off: a host restarted with the flag off must not leave a row running.
func TestARestartWithTheChecksOffStillEndsTheRunningRow(t *testing.T) {
	w := newCW(t)
	w.pass()
	at := w.clk.Now().UTC().Format(time.RFC3339)
	w.exec(`INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, subject_root, started_at, written_by) VALUES (77, ?, 1, 'running', ?, ?, 'host-verifier')`, goalOf(w.pworld).Hash, w.root, at)
	w.advance(time.Minute)
	w.h.Pursuit.Verifier = nil
	w.h.pursuitCheckStart(context.Background())
	if got := w.checks(); len(got) != 1 || got[0] != "1:abandoned::host-restarted" {
		t.Fatalf("checks = %v", got)
	}
}

// A panic in the check, and an error that is not an outcome, end the row abandoned at the cap and rest the mission.
func TestAPanicOrAnErrorInTheCheckEndsItAbandonedAndRestsTheMission(t *testing.T) {
	for name, fn := range map[string]func(context.Context, verifier.Request) (verifier.Result, error){
		"panic": func(context.Context, verifier.Request) (verifier.Result, error) { panic("boom") },
		"error": func(context.Context, verifier.Request) (verifier.Result, error) {
			return verifier.Result{}, errors.New("no scratch")
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newCW(t)
			w.fn = fn
			w.untilChecked(1)
			got := w.checks()
			if len(got) != 1 || got[0][:11] != "1:abandoned" {
				t.Fatalf("checks = %v", got)
			}
			if s := w.spend(); s.Micro != missionpursuit.CheckCapMicro {
				t.Errorf("spend %d, want the cap", s.Micro)
			}
			w.personActs() // even with the person acting the mission rests for a minute
			w.pass()
			w.wait()
			if w.checkRows() != 1 {
				t.Errorf("a second check started inside the rest: %v", w.checks())
			}
		})
	}
}
