package applypark

import (
	"context"
	"errors"
	"testing"
)

// NeedsYou records the reason on the fake and frees its running count.
func (f *fakeRunner) NeedsYou(_ context.Context, id, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.reasons == nil {
		f.reasons = map[string]string{}
	}
	f.reasons[id] = reason
	return nil
}

// endedRunner makes every task end with a status, or every Finalize fail.
type endedRunner struct {
	*fakeRunner
	status      string
	finalizeErr error
}

func (e endedRunner) Wait(context.Context, string) (string, error) { return e.status, nil }
func (e endedRunner) Finalize(context.Context, string) error       { return e.finalizeErr }

func TestQueueFailedOrCancelledTaskBecomesNeedsYou(t *testing.T) {
	for _, status := range []string{"failed", "cancelled"} {
		r, q, ids := queueFixture(t, 1)
		q.Runner = endedRunner{fakeRunner: r, status: status}
		out, err := q.Run(context.Background(), ids)
		if err != nil {
			t.Fatal(err)
		}
		if out[0].Result != ResultFailed || out[0].Error == "" {
			t.Fatalf("%s: outcome %+v", status, out[0])
		}
		if r.reasons[ids[0]] == "" {
			t.Fatalf("%s: application left filling, NeedsYou not called", status)
		}
	}
}

func TestQueueFinalizeErrorBecomesNeedsYou(t *testing.T) {
	r, q, ids := queueFixture(t, 1)
	q.Runner = endedRunner{fakeRunner: r, status: "complete", finalizeErr: errors.New("bad result")}
	out, err := q.Run(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Result != ResultFailed || r.reasons[ids[0]] == "" {
		t.Fatalf("outcome %+v reasons %v", out[0], r.reasons)
	}
}
