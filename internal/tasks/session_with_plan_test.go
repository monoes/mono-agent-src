package tasks

import (
	"errors"
	"testing"
)

// Review 2, smaller (b): the session and its plan's revision come from one read.
func TestSessionWithPlanReadsTheSessionAndItsPlanTogether(t *testing.T) {
	s, _, _ := newTestStore(t)
	p, err := s.CreateSession(bg, "default", SessionInput{Title: "plan"}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSessionTask(bg, "default", p.ID, SessionTaskInput{Title: "a", Acceptance: "done"}, human); err != nil {
		t.Fatal(err)
	}
	se, pv, err := s.SessionWithPlan(bg, "default", p.ID, human)
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.PlanOf(bg, "default", p.ID, human)
	if err != nil {
		t.Fatal(err)
	}
	if se.Title != "plan" || pv != want || pv.TaskCount != 1 || pv.Rev != se.Rev {
		t.Fatalf("session %+v, plan %+v, want %+v", se, pv, want)
	}
	if _, _, err := s.SessionWithPlan(bg, "default", p.ID, Actor{Kind: Agent, Name: "lead", Session: p.ID}); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an agent read the plan digest: %v", err)
	}
}
