package aitask

import (
	"context"
	"time"
)

// Wait blocks until task id is over or needs the user, ctx ends, or timeout
// (when positive) passes. On a timeout it returns the task as it is with a
// CodeWaitTimeout error.
func (s *Service) Wait(ctx context.Context, id string, timeout time.Duration) (Task, error) {
	poll := s.PollEvery
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	var deadline <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	for {
		t, err := s.Store.Get(id)
		if err != nil || Terminal(t.Status) || t.Status == StatusNeedsUser {
			return t, err
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-deadline:
			return t, &Error{Code: CodeWaitTimeout, Message: "task " + id + " is still " + t.Status,
				Fields: map[string]any{"status": t.Status}}
		case <-time.After(poll):
		}
	}
}
