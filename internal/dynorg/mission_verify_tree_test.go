package dynorg

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// seen is what a verifier's run found in its working directory.
type seen struct {
	mu   sync.Mutex
	cwd  string
	file string
}

func (r *missionRig) verifierSees(file string) *seen {
	s := &seen{}
	r.ex.verify = func(ctx context.Context, id int64, o monomind.ExecOptions) (*monomind.TurnResult, error) {
		b, _ := os.ReadFile(filepath.Join(o.Cwd, file))
		s.mu.Lock()
		s.cwd, s.file = o.Cwd, string(b)
		s.mu.Unlock()
		actor := verifierActorOf(r.t, o.Env)
		if _, err := r.store.VerifySessionTask(tbg, "default", actor.Session, id, true, "ok", actor); err != nil {
			return nil, err
		}
		return &monomind.TurnResult{SawDone: true, ResultText: "PASS", StopReason: monomind.StopEndTurn}, nil
	}
	return s
}

// The writer's branch is only in the store (a Conductor of an earlier turn made it): a
// verifier of a later turn still gets the executor's code as its working directory, and the
// checkout goes away with its run.
func TestVerifierOfAnIsolatedWriterRunsInACheckoutOfItsBranchInALaterTurn(t *testing.T) {
	repo := newRepo(t)
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	branchWith(t, repo, "monoagent/t1/w1", "one.txt", "the executor's file\n")
	r.isolatedWriter(a, "monoagent/t1/w1") // the first turn's conductor held it; the store holds the branch now
	got := r.verifierSees("one.txt")

	c2 := r.isolatedConductor(repo, "t2")
	out, err := c2.Handle(tbg, ToolMissionVerify, json.RawMessage(`{"task_id":`+strconv.FormatInt(a.ID, 10)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = out
	eventually(t, "the verdict", func() bool { return r.task(a.ID).Verified })
	got.mu.Lock()
	cwd, file := got.cwd, got.file
	got.mu.Unlock()
	if file != "the executor's file\n" {
		t.Fatalf("the verifier saw %q in %s", file, cwd)
	}
	if !strings.HasPrefix(cwd, filepath.Join(repo, WorktreeDirName, "t2")) {
		t.Fatalf("cwd %s", cwd)
	}
	if !strings.Contains(r.ex.vruns[a.ID][0].Prompt, "monoagent/t1/w1") || !strings.Contains(r.ex.vruns[a.ID][0].Prompt, "checkout") {
		t.Fatalf("the brief does not say where the code is:\n%s", r.ex.vruns[a.ID][0].Prompt)
	}
	eventually(t, "the checkout to be removed", func() bool { _, err := os.Stat(cwd); return os.IsNotExist(err) })
	if st := gitT(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("the chat folder is not clean:\n%s", st)
	}
	if gitT(t, repo, "rev-parse", "--verify", "monoagent/t1/w1") == "" {
		t.Fatal("the branch is gone")
	}
}

// With the lead's writers shared, or a task with no branch, the verifier stays in the chat folder.
func TestVerifierWithoutAnUnmergedBranchStaysInTheChatFolder(t *testing.T) {
	repo := newRepo(t)
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.toReview(a.ID)
	got := r.verifierSees("README")
	c2 := r.isolatedConductor(repo, "t2")
	if _, err := c2.Handle(tbg, ToolMissionVerify, json.RawMessage(`{"task_id":`+strconv.FormatInt(a.ID, 10)+`}`)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the verdict", func() bool { return r.task(a.ID).Verified })
	got.mu.Lock()
	defer got.mu.Unlock()
	if got.cwd != repo {
		t.Fatalf("cwd %s, want the chat folder %s", got.cwd, repo)
	}
}

func TestVerifierRefusesAHostileStoredBranch(t *testing.T) {
	repo := newRepo(t)
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.isolatedWriter(a, "--upload-pack=x")
	got := r.verifierSees("README")
	c2 := r.isolatedConductor(repo, "t2")
	if _, err := c2.Handle(tbg, ToolMissionVerify, json.RawMessage(`{"task_id":`+strconv.FormatInt(a.ID, 10)+`}`)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the verdict", func() bool { return r.task(a.ID).Verified })
	got.mu.Lock()
	defer got.mu.Unlock()
	if got.cwd != repo {
		t.Fatalf("cwd %s", got.cwd)
	}
}
