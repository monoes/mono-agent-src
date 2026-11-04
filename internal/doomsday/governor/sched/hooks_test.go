package sched

import (
	"context"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// markHook wraps the real queue; before runs ahead of MarkAdmitted, fail replaces its answer.
type markHook struct {
	governor.Queue
	before func()
	fail   error
}

func (m *markHook) MarkAdmitted(c context.Context, p string, g, qid int64) error {
	if m.before != nil {
		m.before()
	}
	if m.fail != nil {
		return m.fail
	}
	return m.Queue.MarkAdmitted(c, p, g, qid)
}

// slotsHook wraps the real slots; after runs once a Reserve has succeeded.
type slotsHook struct {
	governor.Slots
	after func()
}

func (s *slotsHook) Reserve(c context.Context, l governor.Limits, limit int, r governor.Request) error {
	err := s.Slots.Reserve(c, l, limit, r)
	if err == nil && s.after != nil {
		s.after()
	}
	return err
}

// wedgedQueue blocks CancelAll until its context ends: a wedged database.
type wedgedQueue struct{ governor.Queue }

func (wedgedQueue) CancelAll(c context.Context, _ string, _ int64) (int, error) {
	<-c.Done()
	return 0, c.Err()
}
