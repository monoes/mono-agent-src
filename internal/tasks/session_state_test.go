package tasks

import (
	"errors"
	"testing"
)

func TestSessionPauseResumeStopLifecycle(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "s")
	mustTask(t, s, "default", se.ID, "a")
	if _, err := s.PauseSession(bg, "default", se.ID, human); !errors.Is(err, ErrNotReady) {
		t.Fatalf("pausing a draft: %v", err)
	}
	mustRun(t, s, "default", se)
	p, err := s.PauseSession(bg, "default", se.ID, bot("lead"))
	if err != nil || p.State != SessionPaused {
		t.Fatalf("pause: %+v %v", p, err)
	}
	if n, _ := s.SessionNext(bg, "default", se.ID, bot("w"), true, 0); n != nil {
		t.Fatal("a paused session handed out a task")
	}
	if _, err := s.ResumeSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.SessionNext(bg, "default", se.ID, bot("w"), true, 0); n == nil {
		t.Fatal("a resumed session hands out tasks")
	}
	if _, err := s.ArchiveSession(bg, "default", se.ID, human); !errors.Is(err, ErrNotReady) {
		t.Fatalf("archiving a running session: %v", err)
	}
	if _, err := s.StopSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResumeSession(bg, "default", se.ID, human); !errors.Is(err, ErrNotReady) {
		t.Fatalf("a stopped session does not resume: %v", err)
	}
	if _, err := s.AddSessionTask(bg, "default", se.ID, SessionTaskInput{Title: "late", Acceptance: "x"}, human); !errors.Is(err, ErrNotReady) {
		t.Fatalf("adding to a stopped session: %v", err)
	}
	ar, err := s.ArchiveSession(bg, "default", se.ID, human)
	if err != nil || !ar.Archived {
		t.Fatalf("archive: %+v %v", ar, err)
	}
	if ss, _ := s.ListSessions(bg, "default", SessionFilter{}, human); len(ss) != 0 {
		t.Fatalf("an archived session is listed: %+v", ss)
	}
	if ss, _ := s.ListSessions(bg, "default", SessionFilter{IncludeArchived: true}, human); len(ss) != 1 {
		t.Fatalf("--all: %+v", ss)
	}
	ev, _ := s.SessionEvents(bg, "default", se.ID, 0, human)
	kinds := ""
	for _, e := range ev {
		kinds += e.Kind + ","
	}
	if kinds != "created,approved,task_added,pause,resume,stop,archived," && kinds != "created,approved,pause,resume,stop,archived," {
		t.Fatalf("session history: %s", kinds)
	}
}

func TestSessionSpendAndBudgetStopsHandOut(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "s")
	mustTask(t, s, "default", se.ID, "a")
	mustRun(t, s, "default", se)
	b := 1.0
	if _, err := s.UpdateSession(bg, "default", se.ID, SessionUpdate{BudgetUSD: &b}, human); err != nil {
		t.Fatal(err)
	}
	got, err := s.AddSessionSpend(bg, "default", se.ID, 0.4, "worker", human)
	if err != nil || got.SpentUSD != 0.4 {
		t.Fatalf("spend: %+v %v", got, err)
	}
	if n, err := s.SessionNext(bg, "default", se.ID, bot("w"), false, 0); err != nil || n == nil {
		t.Fatalf("under budget: %v %v", n, err)
	}
	if _, err := s.AddSessionSpend(bg, "default", se.ID, 0.6, "", human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionNext(bg, "default", se.ID, bot("w"), true, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("a spent budget hands out nothing: %v", err)
	}
	if _, err := s.AddSessionSpend(bg, "default", se.ID, -1, "", human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative spend: %v", err)
	}
}
