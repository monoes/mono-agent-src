package coderhost

import (
	"fmt"
	"sync"
	"testing"
)

func pendingCascadeResumes(t *testing.T, s *Store, conv string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = 'p' AND conversation_id = ? AND kind = 'resume' AND status = 'pending' AND issuer = '' AND turn_id = ?`, conv, cascadeTurn).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Two hosts over one database run their passes at the same moment: the statement itself lets one resume in.
func TestConcurrentCascadePassesQueueOneResume(t *testing.T) {
	f := newHostFix(t)
	stores := []*Store{f.st, NewStore(f.st.db)}
	for round := 0; round < 20; round++ {
		conv := fmt.Sprintf("race-%d", round)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(s *Store) {
				defer wg.Done()
				<-start
				if _, err := s.enqueueCascadeResume("p", conv); err != nil {
					t.Error(err)
				}
			}(stores[i%2])
		}
		close(start)
		wg.Wait()
		if n := pendingCascadeResumes(t, f.st, conv); n != 1 {
			t.Fatalf("round %d: %d pending cascade resumes, want 1", round, n)
		}
	}
}

// A chat whose control queue is full takes no cascade resume.
func TestCascadeResumeRespectsThePendingCap(t *testing.T) {
	f := newHostFix(t)
	for i := 0; i < MaxPendingPerChat; i++ {
		if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: "full", Kind: KindStop}); err != nil {
			t.Fatal(err)
		}
	}
	queued, err := f.st.enqueueCascadeResume("p", "full")
	if err != nil || queued || pendingCascadeResumes(t, f.st, "full") != 0 {
		t.Fatalf("queued=%v err=%v: a resume went into a full queue", queued, err)
	}
}
