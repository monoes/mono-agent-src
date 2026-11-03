package dynorg

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// loggedGit puts a `git` wrapper first on PATH. It appends "placed" or "UNPLACED" to a log for every git process
// that starts, according to whether the placer marked the command's environment, then runs the real git. The
// marker is the fake placement; the real placement (born in a cgroup) is the env-gated test below.
func loggedGit(t *testing.T) (logFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper")
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	logFile = filepath.Join(dir, "log")
	script := "#!/bin/sh\nif [ \"$DD_PLACED\" = 1 ]; then echo \"placed $*\" >> '" + logFile + "'; else echo \"UNPLACED $*\" >> '" + logFile + "'; fi\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logFile
}

func markPlaced(cmd *exec.Cmd) { cmd.Env = append(cmd.Env, "DD_PLACED=1") }

func readLog(t *testing.T, f string) []string {
	t.Helper()
	b, _ := os.ReadFile(f)
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// The one place the conductor starts git applies the placement the context carries, and releases it after the
// command ended; without a placement the command is untouched.
func TestGitRunAppliesAndReleasesThePlacement(t *testing.T) {
	logf := loggedGit(t)
	repo := newRepo(t)
	_ = os.Remove(logf)
	var released atomic.Int32
	ctx := withGitPlacer(context.Background(), func(cmd *exec.Cmd) (func(), error) {
		markPlaced(cmd)
		return func() { released.Add(1) }, nil
	})
	if _, err := gitRun(ctx, repo, "rev-parse", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, logf); len(got) != 1 || !strings.HasPrefix(got[0], "placed") || released.Load() != 1 {
		t.Fatalf("log %v, released %d", got, released.Load())
	}
	if _, err := gitRun(context.Background(), repo, "rev-parse", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if got := readLog(t, logf); len(got) != 2 || !strings.HasPrefix(got[1], "UNPLACED") {
		t.Fatalf("no run open: log %v", got)
	}
}

// A placement that fails starts nothing: git is never run outside the unit as a fallback.
func TestGitRunDoesNotStartGitWhenItCannotBePlaced(t *testing.T) {
	logf := loggedGit(t)
	repo := newRepo(t)
	_ = os.Remove(logf)
	boom := errors.New("unit gone")
	ctx := withGitPlacer(context.Background(), func(*exec.Cmd) (func(), error) { return nil, boom })
	if _, err := gitRun(ctx, repo, "rev-parse", "HEAD"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := readLog(t, logf); len(got) != 0 {
		t.Fatalf("git started anyway: %v", got)
	}
}

// Every git command a begun merge runs (checkpoint, pin, merge, undo, verification) starts placed. Mutant tried:
// dropping `ctx = mo.placed(ctx)` in Merge leaves UNPLACED lines and this test fails.
func TestEveryGitCommandOfABegunMergeIsPlaced(t *testing.T) {
	logf := loggedGit(t)
	r := newDDRig(t)
	dd := r.doomsday()
	dd.Place = func(cmd *exec.Cmd) (func(), error) { markPlaced(cmd); return func() {}, nil }
	c, _ := ddWriterWith(t, r, dd, "pl1")
	_ = os.Remove(logf) // only the merge's own commands count
	res, err := c.Merge(context.Background(), "w1")
	if err != nil || !res.Merged {
		t.Fatalf("merge = %+v, %v", res, err)
	}
	got := readLog(t, logf)
	if len(got) < 4 {
		t.Fatalf("only %d git commands ran: %v", len(got), got)
	}
	for _, l := range got {
		if !strings.HasPrefix(l, "placed") {
			t.Fatalf("a git command of the merge ran outside the unit: %v", got)
		}
	}
}

// A merge whose commands could not be placed is not begun: nothing ran, nothing was registered.
func TestAMergeWithNoPlacementIsNotBegun(t *testing.T) {
	r := newDDRig(t)
	dd := r.doomsday()
	dd.Place = nil
	c, _ := ddWriterWith(t, r, dd, "pl2")
	if _, err := c.Merge(context.Background(), "w1"); !errors.Is(err, ErrMergeStopped) {
		t.Fatalf("err = %v, want ErrMergeStopped", err)
	}
	if ms := r.live(t); len(ms) != 0 {
		t.Errorf("a member was registered: %+v", ms)
	}
}

// With a unit that cannot be placed in, the merge is refused at its start and nothing is left live.
func TestAMergeIsRefusedWhenThePlacementFailsAtItsFirstCommand(t *testing.T) {
	r := newDDRig(t)
	dd := r.doomsday()
	dd.Place = func(*exec.Cmd) (func(), error) { return nil, errors.New("unit gone") }
	c, repo := ddWriterWith(t, r, dd, "pl3")
	before := gitT(t, repo, "rev-parse", "HEAD")
	if res, err := c.Merge(context.Background(), "w1"); err == nil && res.Merged {
		t.Fatal("merged with no unit")
	}
	if gitT(t, repo, "rev-parse", "HEAD") != before {
		t.Error("HEAD moved")
	}
}

// Recovery's git commands (judgement, abort, verification) are placed when a run is open; a placement that
// cannot be had stops the pass before any git command.
func TestRecoveryGitIsPlacedAndAFailedPlacementStopsThePass(t *testing.T) {
	logf := loggedGit(t)
	s := repoScene(t)
	s.seed(2, true)
	_ = os.Remove(logf)
	rec := s.recovery()
	rec.Git = nil // the conductor's own runner, so the context's placement reaches the exec
	rec.PlaceFor = func(context.Context) (GitPlacer, error) {
		return func(cmd *exec.Cmd) (func(), error) { markPlaced(cmd); return func() {}, nil }, nil
	}
	if _, err := rec.Recover(context.Background(), "", s.r.run.Generation); err != nil {
		t.Fatal(err)
	}
	got := readLog(t, logf)
	if len(got) == 0 {
		t.Fatal("recovery ran no git")
	}
	for _, l := range got {
		if !strings.HasPrefix(l, "placed") {
			t.Fatalf("recovery git ran outside the unit: %v", got)
		}
	}

	s2 := repoScene(t)
	s2.seed(2, true)
	_ = os.Remove(logf)
	rec2 := s2.recovery()
	rec2.Git = nil
	rec2.PlaceFor = func(context.Context) (GitPlacer, error) { return nil, errors.New("unit not proven") }
	if _, err := rec2.Recover(context.Background(), "", s2.r.run.Generation); err == nil {
		t.Fatal("recovery went on without a placement")
	}
	if got := readLog(t, logf); len(got) != 0 {
		t.Fatalf("git ran: %v", got)
	}
	if keep := rec2.GuardTurns(context.Background(), s2.repo, func(string) bool { return false }); !keep("any") {
		t.Error("an unplaceable recovery must protect every turn")
	}
}

// ddWriterWith is ddWriter with a chosen DoomsdayMerge.
func ddWriterWith(t *testing.T, r *ddRig, dd *DoomsdayMerge, turn string) (*Conductor, string) {
	t.Helper()
	repo := newRepo(t)
	ex := &fileExec{}
	c := New(context.Background(), Config{
		Cwd: repo, Limits: Limits{MaxAgents: 4, MaxConcurrent: 3},
		Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true,
		Exec: ex.exec, Emit: &recEmitter{}, Writers: WritersIsolated, TurnID: turn, Doomsday: dd,
	})
	t.Cleanup(c.Close)
	spawnWriter(t, c, "write one.txt x")
	c.Wait(context.Background(), nil, 10*time.Second)
	return c, repo
}
