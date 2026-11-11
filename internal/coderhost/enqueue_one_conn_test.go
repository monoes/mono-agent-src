package coderhost

import (
	"testing"
	"time"
)

// A master's repeated stop inserts no row and answers with the pending one. That lookup must run on the write
// transaction's own connection: through the pool it waits for a connection the transaction itself holds, which a pool
// of one never frees (#1602).
func TestEnqueueWithCheckinAnswersARepeatedMasterStopOnAPoolOfOne(t *testing.T) {
	st := newStoreT(t)
	st.db.SetMaxOpenConns(1)
	stop := Command{ProfileID: "p", ConversationID: "c", Kind: KindStop, Issuer: "m1"}
	first, err := st.Enqueue(stop)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		c   Command
		err error
	}
	done := make(chan result, 1)
	go func() {
		c, err := st.EnqueueWithCheckin(stop, Checkin{At: "x", Snapshot: "y"})
		done <- result{c, err}
	}()
	select {
	case r := <-done:
		if r.err != nil || r.c.ID != first.ID {
			t.Fatalf("got command %d, %v; want the pending stop %d", r.c.ID, r.err, first.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the repeated stop waited for a connection its own transaction holds")
	}
}
