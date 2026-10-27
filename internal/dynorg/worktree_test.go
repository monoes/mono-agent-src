package dynorg

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// Isolated writers (#230), against real git in temp folders. No model
// runs: the fake exec writes the files its brief names.

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitRun(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

// newRepo is a repository with one commit holding a.txt and b.txt.
func newRepo(t *testing.T) string {
	t.Helper()
	needGit(t)
	dir := t.TempDir()
	gitT(t, dir, "init", "--quiet")
	gitT(t, dir, "config", "user.name", "Test")
	gitT(t, dir, "config", "user.email", "test@example.com")
	gitT(t, dir, "config", "commit.gpgsign", "false")
	for _, f := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "--quiet", "-m", "base")
	return dir
}

// fileExec is a fake agent exec: a brief "write <file> <text>" writes text
// to file in the run's folder, then it holds for `hold`.
type fileExec struct {
	hold time.Duration
	// meet, when set, makes each run wait until that many runs are in flight
	// at once, so a test about parallelism does not depend on how fast the
	// machine spawns the second worker.
	meet     int32
	metOnce  sync.Once
	metClose sync.Once
	met      chan struct{}
	mu       sync.Mutex
	cwds     []string
	running  int32
	maxRun   int32
}

func (e *fileExec) exec(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
	e.mu.Lock()
	e.cwds = append(e.cwds, o.Cwd)
	e.mu.Unlock()
	n := atomic.AddInt32(&e.running, 1)
	defer atomic.AddInt32(&e.running, -1)
	for {
		m := atomic.LoadInt32(&e.maxRun)
		if n <= m || atomic.CompareAndSwapInt32(&e.maxRun, m, n) {
			break
		}
	}
	on(monomind.Event{Type: monomind.EventStart})
	if e.meet > 0 {
		// A latch: closed the first time that many runs are in flight together, so a
		// worker that arrives late still sees it closed and cannot wait out the deadline.
		e.metOnce.Do(func() { e.met = make(chan struct{}) })
		if n >= e.meet {
			e.metClose.Do(func() { close(e.met) })
		}
		select {
		case <-e.met:
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
		}
	}
	f := strings.Fields(o.Prompt)
	if len(f) == 3 && f[0] == "checkout" {
		// A worker that leaves its branch, then writes.
		if _, err := gitRun(ctx, o.Cwd, "checkout", "--quiet", "-b", "elsewhere"); err != nil {
			return nil, err
		}
		f[0] = "write"
	}
	if len(f) == 3 && f[0] == "write" {
		if err := os.WriteFile(filepath.Join(o.Cwd, f[1]), []byte(f[2]+"\n"), 0o644); err != nil {
			return nil, err
		}
		in, _ := json.Marshal(map[string]string{"file_path": f[1]})
		on(monomind.Event{Type: monomind.EventToolActivity, CoderFields: monomind.CoderFields{Phase: "start", Input: in}, ID: "t1", Name: "Write"})
	}
	select {
	case <-time.After(e.hold):
	case <-ctx.Done():
		return &monomind.TurnResult{SawDone: true, StopReason: monomind.StopCancelled}, nil
	}
	return okTurn("done"), nil
}

func newWriterConductor(t *testing.T, ex *fileExec, cwd, writers, turn string) (*Conductor, *recEmitter) {
	t.Helper()
	em := &recEmitter{}
	c := New(context.Background(), Config{
		Cwd: cwd, Limits: Limits{MaxAgents: 4, MaxConcurrent: 3},
		Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true,
		Exec: ex.exec, Emit: em, Writers: writers, TurnID: turn,
	})
	return c, em
}

func spawnWriter(t *testing.T, c *Conductor, brief string) WorkerInfo {
	t.Helper()
	info, err := c.Spawn(context.Background(), SpawnRequest{Brief: brief, Role: "coder", Access: ProfileCoding})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func notices(em *recEmitter, code string) []string {
	var out []string
	for _, p := range em.find(chatevents.EventNotice) {
		if n := p.(chatevents.NoticePayload); n.Code == code {
			out = append(out, n.Message)
		}
	}
	return out
}

func waitingForLease(em *recEmitter) bool {
	for _, p := range em.find(chatevents.EventAgentStatus) {
		if s := p.(chatevents.AgentStatusPayload); s.To == chatevents.AgentWaitingLease && s.Detail == "write" {
			return true
		}
	}
	return false
}

func branchExists(t *testing.T, repo, branch string) bool {
	_, err := gitRun(context.Background(), repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestIsolatedWritersRunInParallelInTheirOwnWorktrees(t *testing.T) {
	repo := newRepo(t)
	ex := &fileExec{hold: 10 * time.Millisecond, meet: 2}
	c, em := newWriterConductor(t, ex, repo, WritersIsolated, "t1")
	defer c.Close()
	if !c.Isolated() {
		t.Fatalf("not isolated: %v", notices(em, NoticeWritersShared))
	}
	w1 := spawnWriter(t, c, "write one.txt from-w1")
	w2 := spawnWriter(t, c, "write two.txt from-w2")
	if w1.Branch != "monoagent/t1/w1" || w2.Branch != "monoagent/t1/w2" {
		t.Errorf("branches = %q, %q", w1.Branch, w2.Branch)
	}
	c.Wait(context.Background(), nil, 60*time.Second)
	if ex.maxRun != 2 {
		t.Errorf("max running = %d, want both writers at once", ex.maxRun)
	}
	if waitingForLease(em) {
		t.Error("isolated writers must not wait for the write lease")
	}
	base := filepath.Join(realPath(t, repo), WorktreeDirName, "t1")
	want := map[string]bool{filepath.Join(base, "w1"): true, filepath.Join(base, "w2"): true}
	for _, d := range ex.cwds {
		if !want[d] {
			t.Errorf("worker ran in %s, want its own worktree under %s", d, base)
		}
	}
	// The chat folder is untouched, and each branch holds its worker's commit.
	if _, err := os.Stat(filepath.Join(repo, "one.txt")); err == nil {
		t.Error("a writer edited the chat folder")
	}
	if got := gitT(t, repo, "show", "monoagent/t1/w1:one.txt"); got != "from-w1" {
		t.Errorf("w1's branch has %q", got)
	}
	if got := gitT(t, repo, "show", "monoagent/t1/w2:two.txt"); got != "from-w2" {
		t.Errorf("w2's branch has %q", got)
	}
	if st := gitT(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("the worktrees show in git status:\n%s", st)
	}
	// The stage reads the branch from agent.spawned and agent.status.
	sp := em.find(chatevents.EventAgentSpawned)[0].(chatevents.AgentSpawnedPayload)
	if sp.Branch != "monoagent/t1/w1" {
		t.Errorf("spawned branch = %q", sp.Branch)
	}
	for _, p := range em.find(chatevents.EventAgentStatus) {
		if s := p.(chatevents.AgentStatusPayload); s.AgentID == "w1" && s.Branch != "monoagent/t1/w1" {
			t.Errorf("status without the branch: %+v", s)
		}
	}
}

func TestMergeBringsAWritersBranchIn(t *testing.T) {
	repo := newRepo(t)
	c, _ := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "t2")
	defer c.Close()
	spawnWriter(t, c, "write one.txt merged")
	c.Wait(context.Background(), nil, 10*time.Second)
	out, err := c.Handle(context.Background(), ToolMerge, json.RawMessage(`{"agent_id":"w1"}`))
	if err != nil {
		t.Fatal(err)
	}
	var res MergeResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Merged || res.Branch != "monoagent/t2/w1" || len(res.Files) != 1 || res.Files[0] != "one.txt" || res.Commit == "" {
		t.Errorf("merge = %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "one.txt")); string(b) != "merged\n" {
		t.Errorf("one.txt in the chat folder = %q", b)
	}
	if st := gitT(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("tree not clean after the merge:\n%s", st)
	}
	// Merging again changes nothing.
	again, err := c.Merge(context.Background(), "w1")
	if err != nil || !again.Merged || again.Note == "" {
		t.Errorf("second merge = %+v, %v", again, err)
	}
}

func TestMergeConflictIsAToolErrorAndLeavesTheTreeClean(t *testing.T) {
	repo := newRepo(t)
	c, _ := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "t3")
	defer c.Close()
	spawnWriter(t, c, "write a.txt from-worker")
	c.Wait(context.Background(), nil, 10*time.Second)
	// Meanwhile the lead changed the same file and committed it.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("from-lead\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "commit", "--quiet", "-am", "lead")
	head := gitT(t, repo, "rev-parse", "HEAD")

	_, err := c.Handle(context.Background(), ToolMerge, json.RawMessage(`{"agent_id":"w1"}`))
	if err == nil || !strings.Contains(err.Error(), "conflicts") || !strings.Contains(err.Error(), "a.txt") || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("err = %v, want a conflict listing a.txt", err)
	}
	if st := gitT(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("tree not clean after the aborted merge:\n%s", st)
	}
	if got := gitT(t, repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if _, err := gitRun(context.Background(), repo, "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil {
		t.Error("a merge is still in progress")
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "from-lead\n" {
		t.Errorf("a.txt = %q", b)
	}
}

func TestMergeRefusals(t *testing.T) {
	repo := newRepo(t)
	c, _ := newWriterConductor(t, &fileExec{hold: time.Second}, repo, WritersIsolated, "t4")
	defer c.Close()
	spawnWriter(t, c, "write one.txt x")
	if _, err := c.Merge(context.Background(), "w1"); err == nil || !strings.Contains(err.Error(), "still working") {
		t.Errorf("merge of a running writer: %v", err)
	}
	if _, err := c.Spawn(context.Background(), SpawnRequest{Brief: "investigate it", Access: ProfileResearch}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Merge(context.Background(), "w2"); err == nil || !strings.Contains(err.Error(), "no branch") {
		t.Errorf("merge of a researcher: %v", err)
	}
	if _, err := c.Merge(context.Background(), "w9"); err == nil {
		t.Error("merge of an unknown worker")
	}
}

func TestTurnEndRemovesWorktreesAndKeepsUnmergedBranches(t *testing.T) {
	repo := newRepo(t)
	c, em := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "t5")
	spawnWriter(t, c, "write one.txt merged")
	spawnWriter(t, c, "write two.txt unmerged")
	spawnWriter(t, c, "look around") // edits nothing
	c.Wait(context.Background(), nil, 10*time.Second)
	if _, err := c.Merge(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	c.Close()

	if _, err := os.Stat(filepath.Join(repo, WorktreeDirName)); !os.IsNotExist(err) {
		t.Errorf("the worktree folder is still there: %v", err)
	}
	if branchExists(t, repo, "monoagent/t5/w1") || branchExists(t, repo, "monoagent/t5/w3") {
		t.Error("merged or empty branches must go")
	}
	if !branchExists(t, repo, "monoagent/t5/w2") {
		t.Fatal("the unmerged branch was deleted")
	}
	if got := gitT(t, repo, "show", "monoagent/t5/w2:two.txt"); got != "unmerged" {
		t.Errorf("kept branch has %q", got)
	}
	kept := notices(em, NoticeBranchKept)
	if len(kept) != 1 || !strings.Contains(kept[0], "monoagent/t5/w2") {
		t.Errorf("kept notices = %q", kept)
	}
	if list := gitT(t, repo, "worktree", "list", "--porcelain"); strings.Contains(list, WorktreeDirName) {
		t.Errorf("git still lists the worktrees:\n%s", list)
	}
}

func TestReconcileRemovesStaleWorktrees(t *testing.T) {
	repo := newRepo(t)
	base := filepath.Join(repo, WorktreeDirName)
	add := func(turn, w string) string {
		p := filepath.Join(base, turn, w)
		gitT(t, repo, "worktree", "add", "--quiet", "-b", branchName(turn, w), p, "HEAD")
		return p
	}
	crashed := add("old", "w1") // left with uncommitted work by a crash
	if err := os.WriteFile(filepath.Join(crashed, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	add("old", "w2") // nothing in it
	live := add("live", "w1")
	stray := filepath.Join(base, "old", "notatree")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stray, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := ReconcileWorktrees(context.Background(), repo, func(id string) bool { return id == "live" })
	by := map[string]TreeCleanup{}
	for _, r := range res {
		by[r.Turn+"/"+r.Agent] = r
	}
	if r := by["old/w1"]; !r.Removed || !r.BranchKept {
		t.Errorf("old/w1 = %+v, want removed with its branch kept", r)
	}
	if r := by["old/w2"]; !r.Removed || r.BranchKept {
		t.Errorf("old/w2 = %+v, want removed with its branch", r)
	}
	if r := by["old/notatree"]; r.Removed || r.Error == "" {
		t.Errorf("a non-empty folder that is not a worktree must stay: %+v", r)
	}
	if _, ok := by["live/w1"]; ok {
		t.Error("an active turn's worktree was touched")
	}
	if _, err := os.Stat(crashed); !os.IsNotExist(err) {
		t.Error("the stale worktree is still there")
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the live worktree is gone: %v", err)
	}
	if got := gitT(t, repo, "show", "monoagent/old/w1:wip.txt"); got != "wip" {
		t.Errorf("the crashed worker's work was not kept on its branch: %q", got)
	}
	if branchExists(t, repo, "monoagent/old/w2") {
		t.Error("the empty branch should go")
	}
	if _, err := os.Stat(filepath.Join(stray, "keep.txt")); err != nil {
		t.Errorf("the stray folder's file is gone: %v", err)
	}
}

func TestPathGuard(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, WorktreeDirName)
	outside := filepath.Join(root, "outside")
	for _, d := range []string{filepath.Join(base, "t"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(base, "t", "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		base, p string
		want    bool
	}{
		{base, filepath.Join(base, "t", "w1"), true},
		{base, filepath.Join(base, "t"), true},
		{base, base, false},
		{base, root, false},
		{base, filepath.Join(base, "..", "outside"), false},
		{base, filepath.Join(base, "t", "..", "..", "outside"), false},
		{base, filepath.Join(root, WorktreeDirName+"-evil", "t"), false},
		{base, filepath.Join(link, "w1"), false},      // through a symlink that leaves base
		{outside, filepath.Join(outside, "t"), false}, // not a worktree folder
		{"", filepath.Join(base, "t"), false},
	}
	for _, tc := range cases {
		if got := within(tc.base, tc.p); got != tc.want {
			t.Errorf("within(%s, %s) = %v, want %v", tc.base, tc.p, got, tc.want)
		}
	}
	// removeTree refuses a path outside the worktree folder and leaves it.
	victim := filepath.Join(outside, "work")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	r := removeTree(context.Background(), root, base, victim, "monoagent/t/w1")
	if r.Removed || !strings.Contains(r.Error, "outside") {
		t.Errorf("removeTree outside = %+v", r)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("the outside folder was removed: %v", err)
	}
	// A symlinked worktree folder is refused altogether.
	repo := newRepo(t)
	if err := os.Symlink(outside, filepath.Join(repo, WorktreeDirName)); err != nil {
		t.Fatal(err)
	}
	if _, err := safeBase(repo); err == nil {
		t.Error("a symlinked worktree folder must be refused")
	}
}

func TestNonGitFolderFallsBackToTheLease(t *testing.T) {
	needGit(t)
	dir := t.TempDir()
	ex := &fileExec{hold: 100 * time.Millisecond}
	c, em := newWriterConductor(t, ex, dir, WritersIsolated, "t6")
	defer c.Close()
	if c.Isolated() {
		t.Fatal("a folder outside git can't be isolated")
	}
	if n := notices(em, NoticeWritersShared); len(n) != 1 || !strings.Contains(n[0], "not in a git repository") {
		t.Errorf("notices = %q", n)
	}
	w1 := spawnWriter(t, c, "write one.txt x")
	spawnWriter(t, c, "write two.txt y")
	c.Wait(context.Background(), nil, 10*time.Second)
	if w1.Branch != "" || ex.maxRun != 1 || !waitingForLease(em) {
		t.Errorf("writers must take turns under the lease: branch %q, max running %d", w1.Branch, ex.maxRun)
	}
	if _, err := os.Stat(filepath.Join(dir, "one.txt")); err != nil {
		t.Errorf("the writer should edit the chat folder: %v", err)
	}
	if _, err := c.Merge(context.Background(), "w1"); err == nil || !strings.Contains(err.Error(), "not isolated") {
		t.Errorf("merge without isolation: %v", err)
	}
}

func TestSharedWritersAreUnchanged(t *testing.T) {
	repo := newRepo(t)
	for _, writers := range []string{"", WritersShared} {
		ex := &fileExec{hold: 100 * time.Millisecond}
		c, em := newWriterConductor(t, ex, repo, writers, "t7")
		if c.Isolated() || len(notices(em, NoticeWritersShared)) != 0 {
			t.Errorf("%q: isolated or noticed", writers)
		}
		w1 := spawnWriter(t, c, "write one.txt x")
		spawnWriter(t, c, "write two.txt y")
		c.Wait(context.Background(), nil, 10*time.Second)
		c.Close()
		if w1.Branch != "" || ex.maxRun != 1 || !waitingForLease(em) {
			t.Errorf("%q: branch %q, max running %d", writers, w1.Branch, ex.maxRun)
		}
		for _, d := range ex.cwds {
			if d != repo {
				t.Errorf("%q: worker ran in %s, want the chat folder", writers, d)
			}
		}
		sp := em.find(chatevents.EventAgentSpawned)[0].(chatevents.AgentSpawnedPayload)
		if sp.Branch != "" {
			t.Errorf("%q: spawned branch %q", writers, sp.Branch)
		}
		if _, err := os.Stat(filepath.Join(repo, WorktreeDirName)); !os.IsNotExist(err) {
			t.Errorf("%q: a worktree folder was made", writers)
		}
		if b, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude")); strings.Contains(string(b), WorktreeDirName) {
			t.Errorf("%q: the exclude file was changed", writers)
		}
		for _, tool := range ToolSpecs(true) {
			if tool.Name == ToolMerge {
				t.Error("org_merge must only be given with isolated writers")
			}
		}
	}
}

func TestWorkerPromptNamesItsWorktree(t *testing.T) {
	repo := newRepo(t)
	var prompts []string
	var mu sync.Mutex
	ex := &fileExec{}
	c := New(context.Background(), Config{
		Cwd: repo, Limits: Limits{MaxAgents: 2, MaxConcurrent: 2},
		Staffer: &Staffer{Roster: []Model{opus}, Lead: opus}, ReadAccess: true, Emit: &recEmitter{},
		Writers: WritersIsolated, TurnID: "t8",
		Exec: func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
			mu.Lock()
			prompts = append(prompts, o.SystemPrompt)
			mu.Unlock()
			return ex.exec(ctx, o, on)
		},
	})
	defer c.Close()
	spawnWriter(t, c, "write one.txt x")
	c.Wait(context.Background(), nil, 10*time.Second)
	if len(prompts) != 1 || !strings.Contains(prompts[0], "monoagent/t8/w1") || !strings.Contains(prompts[0], filepath.Join(WorktreeDirName, "t8", "w1")) {
		t.Errorf("prompt = %q", prompts)
	}
}

// userWIP leaves the user's own uncommitted work in the chat folder: a
// modified tracked file and an untracked secret.
func userWIP(t *testing.T, repo string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("user wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "secret.env"), []byte("TOKEN=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertWIPUntouched checks the user's work is still uncommitted and HEAD
// didn't move.
func assertWIPUntouched(t *testing.T, repo, head string) {
	t.Helper()
	if got := gitT(t, repo, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved: %s", gitT(t, repo, "log", "--stat", "-1"))
	}
	st := gitT(t, repo, "status", "--porcelain")
	if !strings.Contains(st, "M b.txt") || !strings.Contains(st, "?? secret.env") {
		t.Errorf("the user's work isn't left as it was:\n%s", st)
	}
}

func TestLostGitFileNeverCommitsTheUsersWork(t *testing.T) {
	repo := newRepo(t)
	c, em := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "m1")
	spawnWriter(t, c, "write one.txt x")
	c.Wait(context.Background(), nil, 10*time.Second)
	userWIP(t, repo)
	head := gitT(t, repo, "rev-parse", "HEAD")
	// The worktree loses its .git (the worker removed it, or a sweep did and
	// the worker's next write recreated the folder), and has new work.
	wt := filepath.Join(realPath(t, repo), WorktreeDirName, "m1", "w1")
	if err := os.Remove(filepath.Join(wt, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "more.txt"), []byte("more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := c.Merge(context.Background(), "w1"); err == nil || res.Merged || !strings.Contains(err.Error(), "nothing was merged") {
		t.Errorf("merge = %+v, %v; want a refusal", res, err)
	}
	assertWIPUntouched(t, repo, head)
	c.Close()
	assertWIPUntouched(t, repo, head)
	if _, err := os.Stat(filepath.Join(wt, "more.txt")); err != nil {
		t.Errorf("the worktree's own files were removed: %v", err)
	}
	if len(notices(em, NoticeCheckpointFailed)) == 0 || len(notices(em, NoticeBranchKept)) == 0 {
		t.Errorf("the failure and the kept folder must be journaled: %v", em.find(chatevents.EventNotice))
	}
}

func TestWorkerOnAnotherBranchIsNeverCommittedFor(t *testing.T) {
	repo := newRepo(t)
	c, em := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "m2")
	spawnWriter(t, c, "checkout one.txt x")
	c.Wait(context.Background(), nil, 10*time.Second)
	if n := notices(em, NoticeCheckpointFailed); len(n) != 1 || !strings.Contains(n[0], "elsewhere") {
		t.Errorf("checkpoint notices = %q", n)
	}
	if _, err := c.Merge(context.Background(), "w1"); err == nil {
		t.Error("a worker off its branch must not be merged")
	}
	c.Close()
	// Nothing was committed on either branch, and the dirty worktree stays.
	for _, br := range []string{"elsewhere", "monoagent/m2/w1"} {
		if n := gitT(t, repo, "rev-list", "--count", "HEAD.."+br); n != "0" {
			t.Errorf("%s got %s commit(s)", br, n)
		}
	}
	wt := filepath.Join(realPath(t, repo), WorktreeDirName, "m2", "w1")
	if _, err := os.Stat(filepath.Join(wt, "one.txt")); err != nil {
		t.Errorf("the uncommitted work was removed: %v", err)
	}
	kept := notices(em, NoticeBranchKept)
	if len(kept) != 1 || !strings.Contains(kept[0], "git worktree remove --force "+wt) {
		t.Errorf("the kept notice must say how to clean up: %q", kept)
	}
}

func TestRemoveTreeKeepsAWorktreeWhoseStateCantBeRead(t *testing.T) {
	repo := newRepo(t)
	base := filepath.Join(realPath(t, repo), WorktreeDirName)
	wt := filepath.Join(base, "m3", "w1")
	gitT(t, repo, "worktree", "add", "--quiet", "-b", "monoagent/m3/w1", wt, "HEAD")
	gitT(t, wt, "checkout", "--quiet", "-b", "off") // so the checkpoint commit is refused
	if err := os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := statusPorcelain
	statusPorcelain = func(context.Context, string) (string, error) { return "", os.ErrDeadlineExceeded }
	defer func() { statusPorcelain = orig }()
	r := removeTree(context.Background(), repo, base, wt, "monoagent/m3/w1")
	if r.Removed || !strings.Contains(r.Error, "could not be read") {
		t.Errorf("removeTree = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(wt, "wip.txt")); err != nil {
		t.Errorf("uncommitted work was deleted: %v", err)
	}
}

func TestRemoveTreeKeepsADirtyWorktreeWhoseCommitFails(t *testing.T) {
	repo := newRepo(t)
	base := filepath.Join(realPath(t, repo), WorktreeDirName)
	wt := filepath.Join(base, "m4", "w1")
	gitT(t, repo, "worktree", "add", "--quiet", "-b", "monoagent/m4/w1", wt, "HEAD")
	if err := os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stale index lock makes `git add` fail.
	gitDir := gitT(t, wt, "rev-parse", "--absolute-git-dir")
	if err := os.WriteFile(filepath.Join(gitDir, "index.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r := removeTree(context.Background(), repo, base, wt, "monoagent/m4/w1")
	if r.Removed || !strings.Contains(r.Error, "could not be committed") {
		t.Errorf("removeTree = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(wt, "wip.txt")); err != nil {
		t.Errorf("uncommitted work was deleted: %v", err)
	}
}

func TestReconcileSkipsATurnStillRunningElsewhere(t *testing.T) {
	repo := newRepo(t)
	c, _ := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "live2")
	spawnWriter(t, c, "write one.txt x")
	c.Wait(context.Background(), nil, 10*time.Second)
	// The DB says the turn is over (a reconcile finalized it), but its
	// process still runs and holds the lock.
	if res := ReconcileWorktrees(context.Background(), repo, func(string) bool { return false }); len(res) != 0 {
		t.Errorf("reconcile touched a running turn: %+v", res)
	}
	wt := filepath.Join(realPath(t, repo), WorktreeDirName, "live2", "w1")
	if !isWorktreeRoot(context.Background(), wt) {
		t.Fatal("the running turn's worktree is gone")
	}
	c.Close()
	if _, err := os.Stat(filepath.Join(repo, WorktreeDirName)); !os.IsNotExist(err) {
		t.Errorf("the turn's own cleanup left %v", err)
	}
}

func TestCheckpointIgnoresSigningConfig(t *testing.T) {
	repo := newRepo(t)
	gitT(t, repo, "config", "commit.gpgsign", "true")
	gitT(t, repo, "config", "gpg.program", "false") // signing would fail (or wait on a pinentry)
	c, em := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "m5")
	defer c.Close()
	spawnWriter(t, c, "write one.txt signed")
	c.Wait(context.Background(), nil, 10*time.Second)
	if n := notices(em, NoticeCheckpointFailed); len(n) != 0 {
		t.Fatalf("checkpoint failed: %q", n)
	}
	if got := gitT(t, repo, "show", "monoagent/m5/w1:one.txt"); got != "signed" {
		t.Errorf("branch has %q", got)
	}
}

func TestDetachedHeadKeepsTheMergedBranch(t *testing.T) {
	repo := newRepo(t)
	gitT(t, repo, "checkout", "--quiet", "--detach")
	c, em := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "m6")
	spawnWriter(t, c, "write one.txt x")
	c.Wait(context.Background(), nil, 10*time.Second)
	if _, err := c.Merge(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	c.Close()
	// The merge lives only on the detached HEAD: the branch is what keeps it.
	if !branchExists(t, repo, "monoagent/m6/w1") {
		t.Error("the branch was deleted though only a detached HEAD has its work")
	}
	if len(notices(em, NoticeBranchKept)) != 1 {
		t.Errorf("kept notices = %q", notices(em, NoticeBranchKept))
	}
}

func TestIgnoredFilesRemovedWithTheWorktreeAreJournaled(t *testing.T) {
	repo := newRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", ".gitignore")
	gitT(t, repo, "commit", "--quiet", "-m", "ignore")
	c, em := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "m7")
	spawnWriter(t, c, "write config.local secret")
	c.Wait(context.Background(), nil, 10*time.Second)
	c.Close()
	if n := notices(em, NoticeIgnoredRemoved); len(n) != 1 || !strings.Contains(n[0], "config.local") {
		t.Errorf("ignored notices = %q", n)
	}
}

func TestMergesAreSerializedAndBlockFollowUps(t *testing.T) {
	repo := newRepo(t)
	c, _ := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "m8")
	defer c.Close()
	spawnWriter(t, c, "write one.txt x")
	spawnWriter(t, c, "write two.txt y")
	c.Wait(context.Background(), nil, 10*time.Second)
	// The lead holds the write lease (it is mid-edit), so the lease alone
	// wouldn't keep the two merges apart.
	c.LeadEvent(leadEdit("start", "e1"))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, id := range []string{"w1", "w2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = c.Merge(context.Background(), id)
		}()
	}
	wg.Wait()
	c.LeadEvent(leadEdit("end", "e1"))
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("merges = %v", errs)
	}
	for _, f := range []string{"one.txt", "two.txt"} {
		if _, err := os.Stat(filepath.Join(repo, f)); err != nil {
			t.Errorf("%s not merged: %v", f, err)
		}
	}
	c.mu.Lock()
	c.workers["w1"].merging = true
	c.mu.Unlock()
	if _, err := c.Message(context.Background(), "w1", "more"); err == nil || !strings.Contains(err.Error(), "being merged") {
		t.Errorf("follow-up during a merge: %v", err)
	}
}

func TestMergeKeepsTheLeadsUncommittedWork(t *testing.T) {
	repo := newRepo(t)
	c, _ := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "m9")
	defer c.Close()
	spawnWriter(t, c, "write one.txt x")
	c.Wait(context.Background(), nil, 10*time.Second)
	userWIP(t, repo)
	res, err := c.Merge(context.Background(), "w1")
	if err != nil || !res.Merged {
		t.Fatalf("merge = %+v, %v", res, err)
	}
	st := gitT(t, repo, "status", "--porcelain")
	if !strings.Contains(st, "M b.txt") || !strings.Contains(st, "?? secret.env") {
		t.Errorf("the lead's work changed:\n%s", st)
	}
	if files := gitT(t, repo, "show", "--name-only", "--format=", "HEAD^2"); strings.Contains(files, "secret.env") || strings.Contains(files, "b.txt") {
		t.Errorf("the lead's work was committed: %s", files)
	}
}

func TestGitIgnoresARedirectingEnvironment(t *testing.T) {
	repo := newRepo(t)
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "elsewhere"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	if top := gitT(t, repo, "rev-parse", "--show-toplevel"); top != realPath(t, repo) {
		t.Errorf("git ran against %s", top)
	}
}

func TestWithinRefusesASymlinkedBase(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(real, "t"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, WorktreeDirName)
	if err := os.Symlink(real, base); err != nil {
		t.Fatal(err)
	}
	if within(base, filepath.Join(base, "t")) {
		t.Error("a symlinked worktree folder must be refused")
	}
}

func TestWithIdentityRespectsTheEnvironment(t *testing.T) {
	dir := t.TempDir() // no repository: no config
	needGit(t)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "none"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for k, want := range map[string]string{"": "user.name=mono-agent user.email=mono-agent@localhost", "EMAIL": "user.name=mono-agent"} {
		for _, v := range []string{"EMAIL", "GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
			t.Setenv(v, "")
		}
		if k != "" {
			t.Setenv(k, "me@example.com")
		}
		var got []string
		args := withIdentity(context.Background(), dir, "commit")
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-c" {
				got = append(got, args[i+1])
			}
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%q set: identity = %q, want %q", k, got, want)
		}
	}
}

func TestStoppingTheTurnMidMergeLeavesTheRepoConsistent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook")
	}
	repo := newRepo(t)
	c, _ := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "m10")
	defer c.Close()
	spawnWriter(t, c, "write one.txt x")
	c.Wait(context.Background(), nil, 10*time.Second)
	// A slow content filter keeps git inside the merge while the turn is stopped (hooks are
	// off for the scheduler's git, so a hook cannot be used for this).
	gitT(t, repo, "config", "filter.slow.smudge", "sleep 1; cat")
	gitT(t, repo, "config", "filter.slow.clean", "cat")
	info := gitT(t, repo, "rev-parse", "--absolute-git-dir")
	if err := os.MkdirAll(filepath.Join(info, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "info", "attributes"), []byte("one.txt filter=slow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	res, err := c.Merge(ctx, "w1")
	if ctx.Err() == nil {
		t.Fatal("the turn was not stopped during the merge")
	}
	if err != nil || !res.Merged {
		t.Errorf("merge = %+v, %v; want it to finish", res, err)
	}
	gitDir := gitT(t, repo, "rev-parse", "--absolute-git-dir")
	if _, err := os.Stat(filepath.Join(gitDir, "index.lock")); !os.IsNotExist(err) {
		t.Errorf("index.lock left behind: %v", err)
	}
	if _, err := gitRun(context.Background(), repo, "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil {
		t.Error("a merge is still in progress")
	}
	if st := gitT(t, repo, "status", "--porcelain"); st != "" {
		t.Errorf("tree not clean:\n%s", st)
	}
}

func TestReconcileReportsATurnItCantLock(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions and a non-root user")
	}
	repo := newRepo(t)
	turnDir := filepath.Join(repo, WorktreeDirName, "locked")
	if err := os.MkdirAll(turnDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(turnDir, 0o555); err != nil { // the lock file can't be created
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(turnDir, 0o755) })
	res := ReconcileWorktrees(context.Background(), repo, func(string) bool { return false })
	if len(res) != 1 || res[0].Turn != "locked" || !strings.Contains(res[0].Error, "lock could not be taken") {
		t.Errorf("reconcile = %+v, want the skip reported", res)
	}
}
