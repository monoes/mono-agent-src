package dynorg

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
)

func TestMissionRecordsTheFolderHeadWhenATaskFirstStarts(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "first"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, rigOpts{})
	r.c.cfg.Cwd = repo
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "the worker", func() bool { return r.c.taskWorker(a.ID) != nil })
	eventually(t, "the start point", func() bool {
		d, err := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
		if err != nil {
			return false
		}
		for _, x := range d.Artifacts {
			if x.Kind == tasks.ArtifactBase && x.Ref == strings.TrimSpace(string(head)) && x.Actor != "" {
				return true
			}
		}
		return false
	})
}

func TestMissionRecordsAStandInStartForARepositoryWithNoCommit(t *testing.T) {
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	r := newRig(t, rigOpts{})
	r.c.cfg.Cwd = repo
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	r.next(r.c)
	eventually(t, "the worker", func() bool { return r.c.taskWorker(a.ID) != nil })
	eventually(t, "the stand-in start point", func() bool {
		d, err := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
		if err != nil {
			return false
		}
		for _, x := range d.Artifacts {
			if x.Kind == tasks.ArtifactBase && x.Ref == tasks.BaseUnborn {
				return true
			}
		}
		return false
	})
}
