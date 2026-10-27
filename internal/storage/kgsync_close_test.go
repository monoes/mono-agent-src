package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/monomind"
)

// Close waits for the knowledge-graph syncs still running: they read the
// database it is about to close.
func TestCloseWaitsForRunningKGSyncs(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "kg.db"))
	if err != nil {
		t.Fatal(err)
	}
	release, finished := make(chan struct{}), make(chan struct{})
	db.kg.start(func(func()) {
		<-release
		close(finished)
	})
	closed := make(chan struct{})
	go func() {
		db.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned while a sync was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-closed
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before the sync finished")
	}
}

// A sync stuck in monomind holds Close for about a second, not for the sync:
// the monomind process finishes on its own after this one exits. Nothing
// starts once closing has begun.
func TestKGSyncStopIsBoundedAndFinal(t *testing.T) {
	var k kgSyncs
	block := make(chan struct{})
	defer close(block)
	k.start(func(started func()) {
		started()
		<-block
	})
	begin := time.Now()
	if k.stop() {
		t.Fatal("stop reported every sync done while one was blocked")
	}
	if waited := time.Since(begin); waited < 900*time.Millisecond || waited > 3*time.Second {
		t.Fatalf("stop waited %s for a started sync, want about 1s", waited)
	}
	ran := make(chan struct{}, 1)
	k.start(func(func()) { ran <- struct{}{} })
	select {
	case <-ran:
		t.Fatal("a sync started after closing began")
	case <-time.After(50 * time.Millisecond):
	}
}

// A sync that has not started its monomind process yet is waited for until
// it has, past the second: nothing would send its nodes again.
func TestCloseWaitsForASyncToStart(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "kg.db"))
	if err != nil {
		t.Fatal(err)
	}
	startedAt := make(chan time.Time, 1)
	release := make(chan struct{})
	defer close(release)
	db.kg.start(func(started func()) {
		time.Sleep(1500 * time.Millisecond) // finding and starting monomind on a slow machine
		startedAt <- time.Now()
		started()
		<-release
	})
	begin := time.Now()
	db.Close()
	returned := time.Now()
	select {
	case at := <-startedAt:
		if returned.Before(at) {
			t.Fatal("Close returned before the sync started")
		}
	default:
		t.Fatalf("Close returned after %s, before the sync started", returned.Sub(begin))
	}
	if waited := returned.Sub(begin); waited > 5500*time.Millisecond {
		t.Fatalf("Close waited %s, past its 5s bound", waited)
	}
}

// A sync that ends without starting a monomind process (none found, say)
// counts as started: while another is held open, Close returns after the
// second it gives syncs to finish, not after the 5s it gives one to start.
func TestCloseDoesNotWaitForASyncThatEndedWithoutStarting(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "kg.db"))
	if err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	defer close(block)
	db.kg.start(func(started func()) {
		started()
		<-block
	})
	ended := make(chan struct{})
	db.kg.start(func(func()) { close(ended) })
	<-ended
	begin := time.Now()
	db.Close()
	if waited := time.Since(begin); waited > 3*time.Second {
		t.Fatalf("Close waited %s: it waited for a sync that ended without starting", waited)
	}
}

// Each profile of a batch gets a sync of its own: one held in monomind does
// not keep the next profile's from starting before Close returns.
func TestBatchUpsertPeopleStartsEveryProfilesSync(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
	ran, _ := slowFakeMonomind(t)
	db := newUpsertPersonTestDB(t)
	if err := db.BatchUpsertPeople([]*Person{
		{PlatformUsername: "ada@example.com", Platform: "email", ProfileID: "pa", FullName: "Ada"},
		{PlatformUsername: "bob@example.com", Platform: "email", ProfileID: "pb", FullName: "Bob"},
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, _ := os.ReadFile(ran)
		if lines := strings.Count(string(b), "\n"); lines >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Close returned with only these syncs started: %q", b)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// slowFakeMonomind installs a monomind that logs each start (its
// MONOMIND_CWD) to ran and then waits until the test ends.
func slowFakeMonomind(t *testing.T) (ran, release string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	ran, release = filepath.Join(dir, "ran"), filepath.Join(dir, "release")
	script := "#!/bin/sh\necho \"$MONOMIND_CWD\" >> '" + ran + "'\n" +
		"i=0\nwhile [ ! -e '" + release + "' ] && [ $i -lt 300 ]; do sleep 0.1; i=$((i+1)); done\n"
	bin := filepath.Join(dir, "bin", "monomind")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	return ran, release
}

// End to end, with a monomind that is stuck: saving a person and closing
// returns once Settle's bounds are spent, without waiting for the sync, and the
// sync's monomind still ran. The bounds are shortened and nothing here measures
// seconds: the fake monomind cannot end until the test releases it, which it
// does only after Close has returned, so Close returning at all is the proof
// that it did not wait for the sync (an unbounded Close would block until the
// hang guard fails the test). The 1s finish window itself is pinned by
// TestKGSyncStopIsBoundedAndFinal, without disk or process start-up in the way.
func TestCloseIsBoundedForASlowMonomind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
	ran, release := slowFakeMonomind(t)
	t.Cleanup(monomind.SetSettleBounds(50*time.Millisecond, 200*time.Millisecond))

	db := newUpsertPersonTestDB(t)
	if err := db.UpsertPerson(&Person{PlatformUsername: "ada@example.com", Platform: "email", ProfileID: "default", FullName: "Ada"}); err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() { db.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(30 * time.Second): // a hang guard, not the bound under test
		_ = os.WriteFile(release, nil, 0o600) // let the stuck sync end so the test can clean up
		t.Fatal("Close did not return while a sync was stuck in monomind: it is not bounded")
	}
	// The stuck monomind cannot have ended (it waits for release), so a Close that had
	// waited for the sync to finish would find none running here.
	if db.kg.group.Running() == 0 {
		t.Fatal("Close returned only after the sync finished: it waited for the sync instead of giving up at Settle's bound")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ran); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sync's monomind never ran")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
