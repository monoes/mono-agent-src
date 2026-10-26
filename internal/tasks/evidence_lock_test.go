package tasks

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// evTask is a code task in Review holding the commit artifact "abc" (the store has no
// checker yet, so nothing is validated while it gets there).
func evTask(t *testing.T, s *Store, notes string) (Session, Task) {
	t.Helper()
	se, tk := claimedTask(t, s, notes, "task")
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "commit", Ref: "abc"}, bot("w1")); err != nil {
		t.Fatal(err)
	}
	return se, tk
}

func TestGitCheckDoesNotHoldTheWriteLock(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := evTask(t, s, codeNotes)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.SetEvidenceChecker(func(ctx context.Context, c EvidenceCheck) error {
		once.Do(func() { close(started) })
		<-release // a hung git
		return nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1"))
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the checker never ran")
	}
	ctx, cancel := context.WithTimeout(bg, 3*time.Second)
	defer cancel()
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock() // never leave the finish hanging when the assertion below fails
	if _, _, err := s.Add(ctx, "default", AddInput{Title: "another writer", Ready: true}, human); err != nil {
		t.Fatalf("a writer was blocked while the evidence was being checked: %v", err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatalf("finish: %v", err)
	}
	if st := statusOf(t, s, tk.ID); st != StatusReview {
		t.Fatalf("status %s", st)
	}
}

func TestEvidenceAddedBetweenCheckAndCommitIsRetryable(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := evTask(t, s, codeNotes)
	plain := NewStore(s.db)
	var once sync.Once
	s.SetEvidenceChecker(func(ctx context.Context, c EvidenceCheck) error {
		once.Do(func() { // another process attaches a commit while this one checks
			if _, err := plain.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "commit", Ref: "def"}, bot("w1")); err != nil {
				t.Error(err)
			}
		})
		return nil
	})
	_, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1"))
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("a stale check was used: %v", err)
	}
	if st := statusOf(t, s, tk.ID); st != StatusInProgress {
		t.Fatalf("a refused finish moved the task to %s", st)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1")); err != nil {
		t.Fatalf("the retry: %v", err)
	}
}

func TestEvidenceGateIsBoundedInTimeAndCommits(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := claimedTask(t, s, codeNotes, "task")
	for _, r := range []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7", "c8"} {
		if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "commit", Ref: r}, bot("w1")); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var seen []string
	s.SetEvidenceChecker(func(ctx context.Context, c EvidenceCheck) error {
		if c.Kind == "" {
			return nil
		}
		mu.Lock()
		seen = append(seen, c.Ref)
		mu.Unlock()
		return errors.New("no")
	})
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1")); !errors.Is(err, ErrNoEvidence) {
		t.Fatalf("finish: %v", err)
	}
	if want := []string{"c8", "c7", "c6", "c5", "c4"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("examined %v, want the newest five %v", seen, want)
	}

	// a hung checker is cut off by the one deadline, not once per commit
	old := evidenceBudget
	evidenceBudget = 300 * time.Millisecond
	defer func() { evidenceBudget = old }()
	calls := 0
	s.SetEvidenceChecker(func(ctx context.Context, c EvidenceCheck) error {
		if c.Kind == "" {
			return nil
		}
		calls++
		<-ctx.Done()
		return ctx.Err()
	})
	start := time.Now()
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1")); !errors.Is(err, ErrNoEvidence) {
		t.Fatalf("finish: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second || calls != 1 {
		t.Fatalf("the gate took %v over %d commit checks", d, calls)
	}
}

func TestDeclaredFileAnnotationsAndNone(t *testing.T) {
	got := declaredFiles("Files:\n- src/a.go (new)\n- docs/b.md (renamed from c.md)\n- `x/y.go`\n")
	if want := []string{"src/a.go", "docs/b.md", "`x/y.go`"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("declared %q, want %q", got, want)
	}
	for _, n := range []string{"Files: none", "Files: N/A", "Files:\n- none\n", "x\n\n[mission-meta] {\"files\":[\"none\",\" \"]}", "Files: (new)"} {
		if declaresFiles(n) || len(declaredFiles(n)) != 0 {
			t.Errorf("%q declares %q", n, declaredFiles(n))
		}
	}
	// the two functions agree, so an annotated task is held to the files the checker sees
	n := "Files: src/a.go (new)"
	if !declaresFiles(n) {
		t.Fatal("an annotated entry declares nothing")
	}
	s, _, _ := newTestStore(t)
	se, tk := claimedTask(t, s, n, "task")
	var files []string
	s.SetEvidenceChecker(func(ctx context.Context, c EvidenceCheck) error {
		if c.Kind == "commit" {
			files = c.Files
		}
		return nil
	})
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "commit", Ref: "abc"}, bot("w1")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1")); err != nil {
		t.Fatalf("a task whose files carry annotations: %v", err)
	}
	if !reflect.DeepEqual(files, []string{"src/a.go"}) {
		t.Fatalf("the checker was given %q", files)
	}
	// a task that names none is not a code task
	se2, tk2 := claimedTask(t, s, "Files: none", "task")
	if _, err := s.SessionFinish(bg, "default", se2.ID, tk2.ID, Outcome{Result: "done"}, bot("w1")); err != nil {
		t.Fatalf("Files: none: %v", err)
	}
}

func TestOperatorIsNeverBlockedByTheGitGateOnVerify(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := evTask(t, s, codeNotes)
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done"}, bot("w1")); err != nil {
		t.Fatal(err)
	}
	s.SetEvidenceChecker(func(ctx context.Context, c EvidenceCheck) error { return errors.New("refused") })
	if _, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", bot("v9")); !errors.Is(err, ErrNoEvidence) {
		t.Fatalf("an agent's verify over refused evidence: %v", err)
	}
	if got, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", human); err != nil || !got.Verified {
		t.Fatalf("the operator's verify: %+v %v", got, err)
	}
}

func TestBaseArtifactIsOperatorOnlyAndFirstStands(t *testing.T) {
	s, _, _ := newTestStore(t)
	se, tk := claimedTask(t, s, codeNotes, "task")
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: ArtifactBase, Ref: "abc"}, bot("w1")); !errors.Is(err, ErrOperatorOnly) {
		t.Fatalf("an agent wrote a base: %v", err)
	}
	first, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: ArtifactBase, Ref: "1111"}, human)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: ArtifactBase, Ref: "2222"}, human)
	if err != nil || again.ID != first.ID || again.Ref != "1111" {
		t.Fatalf("a second base replaced the first: %+v %v", again, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM task_artifacts WHERE task_id = ? AND kind = 'base'`, tk.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d base artifacts (%v)", n, err)
	}
}
