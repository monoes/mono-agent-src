package tasks

import (
	"errors"
	"strings"
	"testing"
)

// No session is created below one that is archived or closed (done, stopped, failed), by the operator or
// by an agent; below a draft, running or paused one it is. The message names the parent and its state.
func TestNoSessionIsCreatedBelowAnArchivedOrClosedOne(t *testing.T) {
	run := func(t *testing.T, s *Store, id int64) {
		t.Helper()
		mustTask(t, s, "default", id, "work")
		if _, err := s.ApproveSession(bg, "default", id, human); err != nil {
			t.Fatal(err)
		}
	}
	steps := map[string]func(t *testing.T, s *Store, id int64) error{
		"draft":   func(t *testing.T, s *Store, id int64) error { return nil },
		"running": func(t *testing.T, s *Store, id int64) error { run(t, s, id); return nil },
		"paused": func(t *testing.T, s *Store, id int64) error {
			run(t, s, id)
			_, err := s.PauseSession(bg, "default", id, human)
			return err
		},
		"stopped": func(t *testing.T, s *Store, id int64) error {
			_, err := s.StopSession(bg, "default", id, human)
			return err
		},
		"done": func(t *testing.T, s *Store, id int64) error {
			run(t, s, id)
			_, err := s.CloseSession(bg, "default", id, SessionDone, "", human)
			return err
		},
		"failed": func(t *testing.T, s *Store, id int64) error {
			run(t, s, id)
			_, err := s.CloseSession(bg, "default", id, SessionFailed, "", human)
			return err
		},
		"archived": func(t *testing.T, s *Store, id int64) error {
			if _, err := s.StopSession(bg, "default", id, human); err != nil {
				return err
			}
			_, err := s.ArchiveSession(bg, "default", id, human)
			return err
		},
	}
	for state, put := range steps {
		allowed := state == "draft" || state == "running" || state == "paused"
		for _, who := range []string{"operator", "agent"} {
			t.Run(state+"/"+who, func(t *testing.T) {
				s, _, _ := newTestStore(t)
				p := mustSession(t, s, "default", "parent")
				if err := put(t, s, p.ID); err != nil {
					t.Fatal(err)
				}
				actor := human
				if who == "agent" {
					actor = Actor{Kind: Agent, Name: "lead", Session: p.ID}
				}
				_, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID}, actor)
				if allowed {
					if err != nil {
						t.Fatalf("a session below a %s one: %v", state, err)
					}
					return
				}
				if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "#1") || !strings.Contains(err.Error(), state) {
					t.Fatalf("a session below a %s one: %v", state, err)
				}
			})
		}
	}
}

// An agent outside the parent's subtree still gets not-found, whatever the parent's state.
func TestAnArchivedParentOutsideAnAgentsSubtreeIsNotFound(t *testing.T) {
	s, _, _ := newTestStore(t)
	p := mustSession(t, s, "default", "parent")
	other := mustSession(t, s, "default", "other")
	if _, err := s.ArchiveSession(bg, "default", p.ID, human); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateSession(bg, "default", SessionInput{Title: "kid", ParentSessionID: p.ID}, Actor{Kind: Agent, Name: "x", Session: other.ID})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("an agent outside the subtree: %v", err)
	}
}
