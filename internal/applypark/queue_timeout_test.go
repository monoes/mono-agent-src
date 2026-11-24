package applypark

import (
	"context"
	"testing"
	"time"
)

// cancelRunner records the task the queue cancelled.
type cancelRunner struct {
	*fakeRunner
	cancelled chan string
}

func (c cancelRunner) Cancel(_ context.Context, taskID string) error {
	c.cancelled <- taskID
	return nil
}

// A fill that outlives its ceiling is cancelled and left needs_you; its slot is freed (R10-11).
func TestQueueFillTimeoutCancelsAndNeedsYou(t *testing.T) {
	r, q, ids := queueFixture(t, 1)
	r.gate = make(chan struct{}) // never opens: the agent hangs
	cr := cancelRunner{fakeRunner: r, cancelled: make(chan string, 1)}
	q.Runner = cr
	q.FillTimeout = 30 * time.Millisecond
	out, err := q.Run(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Result != ResultNeedsYou || out[0].Error == "" {
		t.Fatalf("outcome %+v", out[0])
	}
	if r.reasons[ids[0]] == "" {
		t.Fatal("application left filling, NeedsYou not called")
	}
	select {
	case id := <-cr.cancelled:
		if id != "task-"+ids[0] {
			t.Fatalf("cancelled %q", id)
		}
	default:
		t.Fatal("the hung task was not cancelled")
	}
}
