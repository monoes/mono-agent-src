package tasks

import (
	"errors"
	"testing"
)

// Review finding 4: a client id that names a session outside the caller's subtree, or one with another
// parent or kind, is refused, not answered with that session (its goal included).
func TestClientIDDedupeStaysInTheCallersSubtree(t *testing.T) {
	s, _, _ := newTestStore(t)
	secret, err := s.CreateSession(bg, "default", SessionInput{Title: "operator's", Goal: "private goal", ClientID: "key1"}, human)
	if err != nil {
		t.Fatal(err)
	}
	p, lead := carveParent(t, s, 0)
	in := SessionInput{Title: "kid", ParentSessionID: p.ID, ClientID: "key1", Kind: SessionKindResearch}
	got, err := s.CreateSession(bg, "default", in, lead)
	if !errors.Is(err, ErrInvalid) || got.ID == secret.ID || got.Goal != "" {
		t.Fatalf("the operator's session came back: %v %+v", err, got)
	}
	in.ClientID = "key2"
	first, err := s.CreateSession(bg, "default", in, lead)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateSession(bg, "default", in, lead)
	if err != nil || again.ID != first.ID || !again.Deduped {
		t.Fatalf("a repeat of its own call: %v %+v", err, again)
	}
	in.Kind = SessionKindTask
	if _, err := s.CreateSession(bg, "default", in, lead); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another kind under the same key: %v", err)
	}
}
