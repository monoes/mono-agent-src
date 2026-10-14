package aimemory

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/testdb"
)

func contents(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Role + ":" + m.Content
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAppendRecentDeleteClear(t *testing.T) {
	ctx := context.Background()
	db := testdb.Open(t).DB
	for i, r := range []string{"user", "assistant", "human", "ai", "system"} {
		if err := Append(ctx, db, "p1", "s", r, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	all, err := Recent(ctx, db, "p1", "s", 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"human:0", "ai:1", "human:2", "ai:3", "system:4"}; !equal(contents(all), want) {
		t.Fatalf("all = %v, want %v", contents(all), want)
	}
	for i, m := range all {
		if m.Seq != int64(i+1) || m.CreatedAt == "" {
			t.Errorf("message %d = %+v", i, m)
		}
	}
	last, _ := Recent(ctx, db, "p1", "s", 2)
	if want := []string{"ai:3", "system:4"}; !equal(contents(last), want) {
		t.Fatalf("last 2 = %v", contents(last))
	}
	n, err := Delete(ctx, db, "p1", "s", 2)
	if err != nil || n != 2 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	rest, _ := Recent(ctx, db, "p1", "s", 0)
	if want := []string{"human:0", "ai:1", "human:2"}; !equal(contents(rest), want) {
		t.Fatalf("after delete = %v", contents(rest))
	}
	// Appending after a delete continues the order.
	if err := Append(ctx, db, "p1", "s", "ai", "x"); err != nil {
		t.Fatal(err)
	}
	rest, _ = Recent(ctx, db, "p1", "s", 1)
	if rest[0].Content != "x" {
		t.Fatalf("newest = %+v", rest)
	}
	if n, _ := Delete(ctx, db, "p1", "s", 10); n != 4 {
		t.Fatalf("delete more than there are = %d", n)
	}
	if err := Append(ctx, db, "p1", "s", "ai", "y"); err != nil {
		t.Fatal(err)
	}
	if n, err := Clear(ctx, db, "p1", "s"); err != nil || n != 1 {
		t.Fatalf("clear = %d, %v", n, err)
	}
	if got, _ := Recent(ctx, db, "p1", "s", 0); len(got) != 0 {
		t.Fatalf("after clear = %v", contents(got))
	}
}

func TestSessionsAndProfilesAreIsolated(t *testing.T) {
	ctx := context.Background()
	db := testdb.Open(t).DB
	_ = Append(ctx, db, "p1", "a", "human", "a1")
	_ = Append(ctx, db, "p1", "b", "human", "b1")
	_ = Append(ctx, db, "p2", "a", "human", "other profile")
	_ = Append(ctx, db, "", "a", "human", "default profile")
	got, _ := Recent(ctx, db, "p1", "a", 0)
	if want := []string{"human:a1"}; !equal(contents(got), want) {
		t.Fatalf("p1/a = %v", contents(got))
	}
	got, _ = Recent(ctx, db, "default", "a", 0)
	if want := []string{"human:default profile"}; !equal(contents(got), want) {
		t.Fatalf("default/a = %v", contents(got))
	}
	sessions, err := Sessions(ctx, db, "p1")
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions = %+v, %v", sessions, err)
	}
	if _, err := Clear(ctx, db, "p1", "a"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Recent(ctx, db, "p2", "a", 0); len(got) != 1 {
		t.Fatalf("clearing p1/a touched p2/a: %v", contents(got))
	}
}

func TestReplaceAndValidation(t *testing.T) {
	ctx := context.Background()
	db := testdb.Open(t).DB
	_ = Append(ctx, db, "p", "s", "human", "old")
	if err := Replace(ctx, db, "p", "s", []NewMessage{{"system", "be brief"}, {"user", "hi"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := Recent(ctx, db, "p", "s", 0)
	if want := []string{"system:be brief", "human:hi"}; !equal(contents(got), want) {
		t.Fatalf("after replace = %v", contents(got))
	}
	// A bad role rejects the whole batch.
	if err := AppendMany(ctx, db, "p", "s", []NewMessage{{"human", "ok"}, {"robot", "no"}}); err == nil {
		t.Fatal("bad role accepted")
	}
	if err := Replace(ctx, db, "p", "s", []NewMessage{{"tool", "no"}}); err == nil {
		t.Fatal("bad role accepted by replace")
	}
	if got, _ := Recent(ctx, db, "p", "s", 0); len(got) != 2 {
		t.Fatalf("a rejected write changed the session: %v", contents(got))
	}
	if err := Append(ctx, db, "p", " ", "human", "x"); err == nil {
		t.Fatal("empty session key accepted")
	}
	if _, err := Recent(ctx, nil, "p", "s", 0); err == nil {
		t.Fatal("nil db accepted")
	}
}

func TestConcurrentAppendsKeepDistinctOrder(t *testing.T) {
	ctx := context.Background()
	db := testdb.Open(t).DB
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- Append(ctx, db, "p", "s", "human", fmt.Sprint(i))
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := Recent(ctx, db, "p", "s", 0)
	if len(got) != 40 {
		t.Fatalf("stored %d of 40", len(got))
	}
	for i, m := range got {
		if m.Seq != int64(i+1) {
			t.Fatalf("seq %d at %d", m.Seq, i)
		}
	}
}

func TestLockSessionSerialises(t *testing.T) {
	unlock := LockSession("p", "s")
	done := make(chan struct{})
	go func() {
		u := LockSession("p", "s")
		u()
		close(done)
	}()
	other := LockSession("p", "t") // another session is not blocked
	other()
	select {
	case <-done:
		t.Fatal("second lock on the same session did not wait")
	default:
	}
	unlock()
	<-done
	sessionLocks.Lock()
	defer sessionLocks.Unlock()
	if len(sessionLocks.m) != 0 {
		t.Fatalf("locks left behind: %d", len(sessionLocks.m))
	}
}
