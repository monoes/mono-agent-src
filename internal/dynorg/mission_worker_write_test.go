package dynorg

import (
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// A worker of a code task (it declares files to change) edits files: staffing it from the
// words of its brief alone made a "check ... exits 0" acceptance line pick the read-only
// Reviewer, which cannot write the code it was handed.
func TestMissionCodeTaskWorkerIsNotReadOnly(t *testing.T) {
	r := newRig(t, rigOpts{})
	tk, err := r.store.AddSessionTask(tbg, "default", r.sid, tasks.SessionTaskInput{
		Title: "Parse the flag", Notes: "Add the flag.\n\nFiles:\n- cmd/flag.go",
		Acceptance: "check that `go test ./cmd -run TestFlag` exits 0 and review the output", SuggestedExecutor: ExecWorker}, operator)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.store.AddArtifact(tbg, "default", r.sid, tk.ID, tasks.ArtifactInput{Kind: "commit", Ref: "c1"}, operator); err != nil {
		t.Fatal(err)
	}
	r.approve()
	r.next(r.c)
	r.ex.release(tk.ID)
	r.waitStatus(tk.ID, tasks.StatusReview)
	r.ex.mu.Lock()
	got := r.ex.access[tk.ID]
	r.ex.mu.Unlock()
	if got == string(monomind.AccessRead) {
		t.Fatalf("the worker of a code task ran with access %q: it cannot edit its files", got)
	}
}
