package tasks

import (
	"errors"
	"testing"
)

// reviewed makes a running session with one task handed to Review by worker "w1".
func reviewed(t *testing.T, s *Store, worker Actor) (Session, Task) {
	t.Helper()
	se := mustSession(t, s, "default", "rev")
	tk := mustTask(t, s, "default", se.ID, "work")
	mustRun(t, s, "default", se)
	w := worker
	w.Session = se.ID
	if w.Task != 0 {
		w.Task = tk.ID
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, w, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "did it"}, w); err != nil {
		t.Fatal(err)
	}
	return se, tk
}

func notVerified(t *testing.T, s *Store, id int64) {
	t.Helper()
	got, _, err := s.Get(bg, "default", id)
	if err != nil || got.Verified || got.Status != StatusReview {
		t.Fatalf("task changed: %+v %v", got, err)
	}
}

// Finding 1: a worker bound to the task cannot verify it under another name.
func TestBoundWorkerCannotVerifyItsTask(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := reviewed(t, s, Actor{Kind: Agent, Name: "w1", Task: 7})
	for _, name := range []string{"w1", "w2", "agent:x#ab12"} {
		w := Actor{Kind: Agent, Name: name, Session: se.ID, Task: tk.ID}
		if _, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", w); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("%s: want a binding refusal, got %v", name, err)
		}
	}
	notVerified(t, s, tk.ID)
}

// Finding 1: the same agent identity (the MCP name carries #<4 hex>) cannot verify what it did.
func TestSameIdentityCannotVerify(t *testing.T) {
	cases := []struct{ worker, verifier string }{
		{"agent:claude#ab12", "agent:claude#cd34"},
		{"agent:claude", "agent:claude#cd34"},
		{"agent:claude#ab12", "agent:claude"},
		{"w1", "w1"},
		{"Agent:Claude#ab12", "agent:claude#ab12"},
	}
	for _, c := range cases {
		s, _, _ := newTestStore(t)
		se, tk := reviewed(t, s, Actor{Kind: Agent, Name: c.worker})
		v := Actor{Kind: Agent, Name: c.verifier, Session: se.ID}
		if _, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", v); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s verifying %s: %v", c.verifier, c.worker, err)
		}
		notVerified(t, s, tk.ID)
	}
	s, _, _ := newTestStore(t)
	se, tk := reviewed(t, s, Actor{Kind: Agent, Name: "agent:claude#ab12"})
	if _, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", Actor{Kind: Agent, Name: "agent:codex#cd34", Session: se.ID}); err != nil {
		t.Fatalf("another client verifies: %v", err)
	}
}

// Finding 1: a verifier bound to another task is refused; the operator may verify.
func TestVerifierOfAnotherTaskAndOperator(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := reviewed(t, s, Actor{Kind: Agent, Name: "w1"})
	v := Actor{Kind: Agent, Name: "v", Session: se.ID, Task: tk.ID + 1, Verifier: true}
	if _, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", v); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("verifier of another task: %v", err)
	}
	notVerified(t, s, tk.ID)
	if got, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "ok", human); err != nil || !got.Verified {
		t.Fatalf("operator: %+v %v", got, err)
	}
}

// Finding 3: an agent cannot verify while the session is paused; the operator can.
func TestVerifyRefusedWhilePaused(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := reviewed(t, s, Actor{Kind: Agent, Name: "w1"})
	if _, err := s.PauseSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	v := Actor{Kind: Agent, Name: "checker", Session: se.ID}
	if _, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", v); !errors.Is(err, ErrNotReady) {
		t.Fatalf("paused: %v", err)
	}
	if _, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, false, "bad", v); !errors.Is(err, ErrNotReady) {
		t.Fatalf("paused fail: %v", err)
	}
	notVerified(t, s, tk.ID)
	if _, err := s.ResumeSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if got, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", v); err != nil || !got.Verified {
		t.Fatalf("resumed: %+v %v", got, err)
	}
}

func allowDelegation(t *testing.T, s *Store) {
	t.Helper()
	if err := s.SetAgentAccess(bg, "default", AgentAccess{View: true, Approve: true}, human); err != nil {
		t.Fatal(err)
	}
}

// Finding 2: a delegated approval cannot move a task of a draft session to Ready.
func TestDelegatedApproveRefusesSessionTasks(t *testing.T) {
	s, _, _ := newTestStore(t)
	allowDelegation(t, s)
	se := mustSession(t, s, "default", "draft")
	tk := mustTask(t, s, "default", se.ID, "planned by the operator")
	before, _, _ := s.Get(bg, "default", tk.ID)
	if _, err := s.Approve(bg, "default", []int64{tk.ID}, false, bot("deleg")); err == nil {
		t.Fatal("a delegated agent approved a task of a draft session")
	}
	after, _, _ := s.Get(bg, "default", tk.ID)
	if after.Status != before.Status || after.Status == StatusReady {
		t.Fatalf("task moved: %s -> %s", before.Status, after.Status)
	}
	if got := mustSessionState(t, s, se.ID); got != SessionDraft {
		t.Fatalf("session %s", got)
	}
}

// Finding 2: a worker bound to a session or a task reaches neither the personal board's
// Approve nor its Board, even when the operator delegated both.
func TestBoundWorkerCannotReachPersonalBoard(t *testing.T) {
	s, _, _ := newTestStore(t)
	allowDelegation(t, s)
	personal, _, err := s.Add(bg, "default", AddInput{Title: "mine"}, human)
	if err != nil {
		t.Fatal(err)
	}
	se, ts := runningWith(t, s, "a")
	for _, w := range []Actor{
		{Kind: Agent, Name: "w", Session: se.ID},
		{Kind: Agent, Name: "w", Session: se.ID, Task: ts[0].ID},
		{Kind: Agent, Name: "v", Session: se.ID, Task: ts[0].ID, Verifier: true},
	} {
		if _, err := s.Approve(bg, "default", []int64{personal.ID}, false, w); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("approve %+v: %v", w, err)
		}
		if _, err := s.Board(bg, "default", 10, w); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("board %+v: %v", w, err)
		}
	}
	if got, _, _ := s.Get(bg, "default", personal.ID); got.Status != StatusInbox {
		t.Fatalf("personal task moved: %s", got.Status)
	}
	if _, err := s.Board(bg, "default", 10, bot("deleg")); err != nil {
		t.Fatalf("unbound delegate still views: %v", err)
	}
}

// Finding 4: an agent names only a conversation of the profile that exists; the operator is free.
func TestAgentSessionConversationMustExist(t *testing.T) {
	s, db, _ := newTestStore(t)
	other := addProfile(t, db, "other")
	for id, prof := range map[string]string{"conv-mine": "default", "conv-theirs": other} {
		if _, err := db.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES (?, ?, 'x', '', '')`, id, prof); err != nil {
			t.Fatal(err)
		}
	}
	lead := Actor{Kind: Agent, Name: "lead"}
	for _, conv := range []string{"conv-nope", "conv-theirs"} {
		if _, err := s.CreateSession(bg, "default", SessionInput{Title: "t", ConversationID: conv}, lead); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", conv, err)
		}
	}
	if got, _ := s.ListSessions(bg, "default", SessionFilter{}, human); len(got) != 0 {
		t.Fatalf("a refused call created %d sessions", len(got))
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "t", ConversationID: "conv-mine"}, lead); err != nil {
		t.Fatalf("own conversation: %v", err)
	}
	if _, err := s.CreateSession(bg, "default", SessionInput{Title: "t2", ConversationID: "whatever"}, human); err != nil {
		t.Fatalf("operator: %v", err)
	}
}

// Finding 5: a task-bound or verifier actor starts no session, even with no session binding.
func TestTaskBoundActorCannotCreateSession(t *testing.T) {
	s, _, _ := newTestStore(t)
	for _, a := range []Actor{
		{Kind: Agent, Name: "w", Task: 3},
		{Kind: Agent, Name: "v", Task: 3, Verifier: true},
		{Kind: Agent, Name: "v", Verifier: true},
	} {
		if _, err := s.CreateSession(bg, "default", SessionInput{Title: "t"}, a); !errors.Is(err, ErrOperatorOnly) {
			t.Fatalf("%+v: %v", a, err)
		}
	}
	if got, _ := s.ListSessions(bg, "default", SessionFilter{}, human); len(got) != 0 {
		t.Fatalf("%d sessions created", len(got))
	}
}
