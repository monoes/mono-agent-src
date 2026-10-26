package dynorg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

// branchWith makes a branch in repo holding one commit that writes file, and returns to the
// branch the repo was on.
func branchWith(t *testing.T, repo, branch, file, text string) {
	t.Helper()
	cur := gitT(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	gitT(t, repo, "checkout", "--quiet", "-b", branch)
	if err := os.WriteFile(filepath.Join(repo, file), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "--quiet", "-m", "work on "+file)
	gitT(t, repo, "checkout", "--quiet", cur)
}

func (r *missionRig) isolatedConductor(repo, turn string) *Conductor {
	c := New(tbg, Config{Cwd: repo, Limits: Limits{MaxAgents: 20, MaxConcurrent: 5}, Staffer: &Staffer{Roster: []Model{opus}, Lead: opus},
		ReadAccess: true, Exec: r.ex.exec, Emit: r.em, Missions: r.mc, Writers: WritersIsolated, TurnID: turn})
	r.t.Cleanup(c.Close)
	if !c.Isolated() {
		r.t.Fatal("writers are not isolated")
	}
	return c
}

func (r *missionRig) mergeTool(c *Conductor, id string) (MergeResult, error) {
	out, err := c.Handle(tbg, ToolMerge, json.RawMessage(`{"agent_id":"`+id+`"}`))
	var res MergeResult
	if err == nil {
		if e := json.Unmarshal([]byte(out), &res); e != nil {
			r.t.Fatal(e)
		}
	}
	return res, err
}

func (r *missionRig) mergeArtifacts(id int64) int {
	d, _ := r.store.SessionTask(tbg, "default", r.sid, id, operator)
	n := 0
	for _, a := range d.Artifacts {
		if a.Kind == tasks.ArtifactMerge {
			n++
		}
	}
	return n
}

// A new Conductor (a later turn) holds no worker: org_merge finds the writer's branch in the
// store, merges it, and records it, so mission_finish stops asking for it.
func TestOrgMergeInANewTurnMergesTheBranchTheStoreRecorded(t *testing.T) {
	repo := newRepo(t)
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	branchWith(t, repo, "monoagent/t1/w1", "one.txt", "from the writer\n")
	w := r.isolatedWriter(a, "monoagent/t1/w1")
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	r.verified(a.ID)

	c2 := r.isolatedConductor(repo, "t2")
	plan, _ := r.store.PlanOfSession(tbg, "default", r.sid, operator)
	if got := c2.mergesRemaining(tbg, r.mc, r.sid, plan.Tasks); len(got) != 1 {
		t.Fatalf("before: %v", got)
	}
	res, err := r.mergeTool(c2, w.id)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Merged || res.Branch != "monoagent/t1/w1" || len(res.Files) != 1 || res.Files[0] != "one.txt" {
		t.Fatalf("merge %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "one.txt")); string(b) != "from the writer\n" {
		t.Fatalf("one.txt = %q", b)
	}
	if got := c2.mergesRemaining(tbg, r.mc, r.sid, plan.Tasks); len(got) != 0 {
		t.Fatalf("after: %v", got)
	}
	if n := r.mergeArtifacts(a.ID); n != 1 {
		t.Fatalf("merge artifacts %d", n)
	}
	if st := gitT(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("tree not clean:\n%s", st)
	}
}

func TestOrgMergeInANewTurnRefusesClearlyAndRecordsNothing(t *testing.T) {
	repo := newRepo(t)
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.verdicts(func(int64) (bool, string) { return true, "ok" })
	branchWith(t, repo, "monoagent/t1/w1", "a.txt", "writer's version\n")
	w := r.isolatedWriter(a, "monoagent/t1/w1")
	if _, err := r.verify(a.ID); err != nil {
		t.Fatal(err)
	}
	r.verified(a.ID)
	// the chat folder changed the same file since
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("lead's version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "commit", "--quiet", "-am", "lead edit")
	head := gitT(t, repo, "rev-parse", "HEAD")

	c2 := r.isolatedConductor(repo, "t2")
	if _, err := r.mergeTool(c2, w.id); err == nil || !strings.Contains(err.Error(), "conflicts in 1 file(s): a.txt") {
		t.Fatalf("conflict: %v", err)
	}
	if n := r.mergeArtifacts(a.ID); n != 0 || gitT(t, repo, "rev-parse", "HEAD") != head {
		t.Fatalf("a refused merge recorded %d or moved HEAD", n)
	}
	if _, err := r.mergeTool(c2, "w77"); err == nil || !strings.Contains(err.Error(), `no worker "w77"`) {
		t.Fatalf("unknown id: %v", err)
	}
	gitT(t, repo, "branch", "-D", "monoagent/t1/w1")
	if _, err := r.mergeTool(c2, w.id); err == nil || !strings.Contains(err.Error(), "no longer exists") {
		t.Fatalf("lost branch: %v", err)
	}
	if n := r.mergeArtifacts(a.ID); n != 0 {
		t.Fatalf("recorded %d", n)
	}
}

// The branch the store holds is data: a hostile ref (an option, a path, a branch that is not a
// writer's) never reaches git.
func TestOrgMergeRefusesAStoredBranchThatIsNotOurs(t *testing.T) {
	for _, ref := range []string{"--upload-pack=x", "../x", "main", "refs/heads/main", "monoagent/t1/../x", "monoagent/t1"} {
		t.Run(ref, func(t *testing.T) {
			repo := newRepo(t)
			r := newRig(t, rigOpts{})
			a := r.add(spec{title: "a", files: "a.go"})
			r.approve()
			r.verdicts(func(int64) (bool, string) { return true, "ok" })
			w := r.isolatedWriter(a, ref)
			if _, err := r.verify(a.ID); err != nil {
				t.Fatal(err)
			}
			r.verified(a.ID)
			head := gitT(t, repo, "rev-parse", "HEAD")
			c2 := r.isolatedConductor(repo, "t2")
			if _, err := r.mergeTool(c2, w.id); err == nil || !strings.Contains(err.Error(), "not one of ours") {
				t.Fatalf("merge of %q: %v", ref, err)
			}
			if gitT(t, repo, "rev-parse", "HEAD") != head || r.mergeArtifacts(a.ID) != 0 {
				t.Fatal("a refused merge changed something")
			}
		})
	}
}
