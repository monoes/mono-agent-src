package dynorg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// scene is a journal that a dead host left, over a real git repository.
type scene struct {
	t                     *testing.T
	r                     *ddRig
	repo, branch, op      string
	before, tip, fp       string
	gone, recorded        bool
	failAbort, noopAbort  bool
	calls                 []string
	recordedAsked, gaveUp int
}

func newScene(t *testing.T, repo, before, tip, fp string) *scene {
	return &scene{t: t, r: newDDRig(t), repo: repo, branch: "monoagent/t1/w1", op: "merge-dead-1", before: before, tip: tip, fp: fp, gone: true}
}

// seed writes the rows a host wrote before it died: the claim, the step rows up to step, and the pin.
func (s *scene) seed(step int, pinned bool) {
	s.t.Helper()
	ctx := context.Background()
	dd := s.r.doomsday()
	if err := dd.Members.Register(ctx, store.Member{Profile: "p", Generation: s.r.run.Generation, ID: s.op, Kind: "merge", Pid: 4242, Stamp: "host"}); err != nil {
		s.t.Fatal(err)
	}
	must := func(err error) {
		s.t.Helper()
		if err != nil {
			s.t.Fatal(err)
		}
	}
	must(dd.appendRetry(ctx, kindBegin, mergeRec{Op: s.op, Repo: s.repo, Branch: s.branch, Worker: "w1", Step: 1}))
	if pinned {
		must(dd.appendRetry(ctx, kindStep, mergeRec{Op: s.op, Step: 2}))
		must(dd.appendRetry(ctx, kindPin, mergeRec{Op: s.op, Branch: s.branch, Before: s.before, Tip: s.tip, Ref: "refs/heads/" + s.branch, FP: s.fp}))
	}
	for n := 3; n <= step; n++ {
		must(dd.appendRetry(ctx, kindStep, mergeRec{Op: s.op, Step: n}))
	}
}

func (s *scene) git(ctx context.Context, dir string, args ...string) (string, error) {
	s.calls = append(s.calls, strings.Join(args, " "))
	switch args[0] {
	case "reset", "clean", "checkout", "branch", "worktree", "rm", "stash", "update-ref":
		s.t.Errorf("recovery ran a forbidden git command: %v", args)
	}
	if s.failAbort && len(args) >= 2 && args[0] == "merge" && args[1] == "--abort" {
		return "", errors.New("abort failed")
	}
	if s.noopAbort && len(args) >= 2 && args[0] == "merge" && args[1] == "--abort" {
		return "", nil // git says it aborted; the repository is unchanged
	}
	return gitRun(ctx, dir, args...)
}

func (s *scene) recovery() MergeRecovery {
	return MergeRecovery{
		Profile: "p", Members: s.r.st, Journal: s.r.journal(), Git: s.git,
		Gone:     func(store.Member) (bool, error) { return s.gone, nil },
		Recorded: func(context.Context, string) (bool, error) { s.recordedAsked++; return s.recorded, nil },
	}
}

func (s *scene) recover() Recovered {
	s.t.Helper()
	out, err := s.recovery().Recover(context.Background(), "", s.r.run.Generation)
	if err != nil {
		s.t.Fatal(err)
	}
	return out
}

func (s *scene) view() *opView {
	s.t.Helper()
	rows, err := s.r.journal().MergeRows(context.Background(), "p")
	if err != nil {
		s.t.Fatal(err)
	}
	ops, _ := foldJournal(rows)
	if ops[s.op] == nil {
		s.t.Fatalf("no operation %s in the journal", s.op)
	}
	return ops[s.op]
}

func (s *scene) want(state, repair string) {
	s.t.Helper()
	o := s.view()
	if !o.Ended || o.End.State != state || o.End.Repair != repair {
		s.t.Fatalf("operation = ended:%v %+v, want %s/%s (git calls: %v)", o.Ended, o.End, state, repair, s.calls)
	}
}

// repoScene makes a clean repository with a branch holding one commit, pinned as a merge would pin it.
func repoScene(t *testing.T) *scene {
	t.Helper()
	repo := newRepo(t)
	gitT(t, repo, "checkout", "-q", "-b", "feat")
	if err := os.WriteFile(filepath.Join(repo, "c.txt"), []byte("feat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "feat")
	tip := gitT(t, repo, "rev-parse", "HEAD")
	gitT(t, repo, "checkout", "-q", "-")
	if err := os.WriteFile(filepath.Join(repo, "canary.txt"), []byte("my own uncommitted edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return newScene(t, repo, gitT(t, repo, "rev-parse", "HEAD"), tip, repoFingerprint(context.Background(), repo))
}

func (s *scene) canaryIntact() bool {
	b, err := os.ReadFile(filepath.Join(s.repo, "canary.txt"))
	return err == nil && string(b) == "my own uncommitted edit\n"
}

// X1/X2: the host died before a pin was durable. Nothing of the merge step started: clean, repository untouched.
func TestX1X2NoPinIsCleanAndAMergeStateWeDidNotPinIsLeftAlone(t *testing.T) {
	s := repoScene(t)
	s.seed(1, false)
	s.recover()
	s.want(StateIncomplete, RepairClean)
	if !s.canaryIntact() {
		t.Error("the canary edit changed")
	}

	s = repoScene(t)
	s.seed(1, false)
	if err := os.WriteFile(filepath.Join(s.repo, ".git", "index.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s.recover()
	s.want(StateIncomplete, RepairNeedsOperator)
	if _, err := os.Stat(filepath.Join(s.repo, ".git", "index.lock")); err != nil {
		t.Error("recovery removed index.lock")
	}
}

// T11 (X3): pin durable, merge step not started.
func TestT11PinnedButNotStartedIsCleanIfTheRepositoryMatchesAndNeedsTheOperatorIfNot(t *testing.T) {
	s := repoScene(t)
	s.seed(2, true)
	s.recover()
	s.want(StateIncomplete, RepairClean)

	s = repoScene(t)
	s.seed(2, true)
	if err := os.WriteFile(filepath.Join(s.repo, "a.txt"), []byte("someone else edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.recover()
	s.want(StateIncomplete, RepairNeedsOperator)
	if b, _ := os.ReadFile(filepath.Join(s.repo, "a.txt")); string(b) != "someone else edited\n" {
		t.Error("the edit was changed")
	}
}

// T8 (X4a): a conflicting merge killed with MERGE_HEAD present is aborted and the abort verified against the pin.
func TestT8ARealKilledConflictingMergeIsAbortedAndVerified(t *testing.T) {
	k := killMergeInRepo(t, true)
	s := newScene(t, k.repo, k.before, k.tip, k.fp)
	s.seed(3, true)
	s.recover()
	s.want(StateIncomplete, RepairRolledBack)
	head, mh, lock, _ := repoState(t, k.repo)
	if head != k.before || mh || lock {
		t.Errorf("after recovery: head==before:%v MERGE_HEAD:%v index.lock:%v", head == k.before, mh, lock)
	}
	if got := repoFingerprint(context.Background(), k.repo); got != k.fp {
		t.Error("the tree does not match the pin after the abort")
	}
}

// T9: the abort fails: needs-operator, the repository is not touched further (the seam forbids reset, clean, ...).
func TestT9AFailedAbortLeavesTheRepositoryForTheOperator(t *testing.T) {
	k := killMergeInRepo(t, true)
	s := newScene(t, k.repo, k.before, k.tip, k.fp)
	s.failAbort = true
	s.seed(3, true)
	s.recover()
	s.want(StateIncomplete, RepairNeedsOperator)
	if _, mh, _, _ := repoState(t, k.repo); !mh {
		t.Error("MERGE_HEAD is gone although the abort failed")
	}
	if !strings.Contains(s.view().End.Note, "merge --abort") {
		t.Errorf("the note does not name the command: %q", s.view().End.Note)
	}
}

// An abort that reports success but did not restore the pin is not believed: the result is read back.
func TestT9AnAbortThatDidNotRestoreThePinIsNotBelieved(t *testing.T) {
	k := killMergeInRepo(t, true)
	s := newScene(t, k.repo, k.before, k.tip, k.fp)
	s.noopAbort = true
	s.seed(3, true)
	s.recover()
	s.want(StateIncomplete, RepairNeedsOperator)
}

// T10 (X4b, X4c): a tree changed with no merge state (real git: a clean merge killed before its commit), and a
// stale index.lock. Both are left alone; a canary edit is byte-identical and the lock is still there.
func TestT10AnUnexplainedTreeOrALockIsNeverRepaired(t *testing.T) {
	k := killMergeInRepo(t, false)
	if err := os.WriteFile(filepath.Join(k.repo, "canary.txt"), []byte("my own uncommitted edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newScene(t, k.repo, k.before, k.tip, "fingerprint taken before the merge started")
	s.seed(3, true)
	s.recover()
	s.want(StateIncomplete, RepairNeedsOperator)
	if !s.canaryIntact() {
		t.Error("the canary edit changed")
	}
	if _, _, _, status := repoState(t, k.repo); !strings.Contains(status, "A  c.txt") {
		t.Errorf("the staged tree was touched: %q", status)
	}

	s = repoScene(t)
	s.seed(3, true)
	lock := filepath.Join(s.repo, ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s.recover()
	s.want(StateIncomplete, RepairNeedsOperator)
	if _, err := os.Stat(lock); err != nil || !s.canaryIntact() {
		t.Error("the lock was removed or the canary changed")
	}
}

// T12 (X5, X6): the merge commit exists. The store is never written by recovery: applied-unrecorded when it does
// not say so, finished when it does.
func TestT12AMergeThatLandedIsAppliedUnrecordedOrFinishedAndTheStoreIsNeverWritten(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		s := repoScene(t)
		s.recorded = recorded
		s.seed(3, true)
		gitT(t, s.repo, "-c", "user.name=T", "-c", "user.email=t@example.com", "merge", "--no-ff", "-q", "-m", "m", s.tip)
		before := countRows(t, s.r, "task_artifacts")
		s.recover()
		if recorded {
			s.want(StateFinished, "")
		} else {
			s.want(StateIncomplete, RepairAppliedUnrecorded)
		}
		if countRows(t, s.r, "task_artifacts") != before || s.recordedAsked != 1 {
			t.Errorf("recorded=%v: the store was written, or asked %d times", recorded, s.recordedAsked)
		}
		if !s.canaryIntact() {
			t.Error("the canary edit changed")
		}
	}
}

func countRows(t *testing.T, r *ddRig, table string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// T14 (X7): HEAD moved to a commit that neither is the pinned head nor holds the tip.
func TestT14AMovedHeadNeedsTheOperator(t *testing.T) {
	s := repoScene(t)
	s.seed(3, true)
	gitT(t, s.repo, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "someone else")
	s.recover()
	s.want(StateIncomplete, RepairNeedsOperator)
}

// T16 and the P1: a process of the operation not proven gone means no repository access at all (zero git calls),
// the member stays live and blocks quiescence, and a later pass revisits it; only a proof releases it.
func TestT16AnUnprovenProcessMeansNoGitAndAMemberThatStaysLiveUntilProven(t *testing.T) {
	ctx := context.Background()
	s := repoScene(t)
	s.gone = false
	s.seed(3, true)
	for i := 0; i < 2; i++ { // the second pass is the "later start": it repeats the check, and writes nothing new
		out := s.recover()
		if len(out.StillLive) != 1 || len(s.calls) != 0 {
			t.Fatalf("pass %d: still live %v, git calls %v", i, out.StillLive, s.calls)
		}
	}
	if n := strings.Count(strings.Join(s.r.audits(t), "\n"), kindUnproven); n != 1 {
		t.Errorf("%d unproven rows, want 1", n)
	}
	if o := s.view(); !o.Open() {
		t.Error("an unproven operation was ended")
	}
	for _, to := range []store.RunState{store.StateStopRequested, store.StateAuthorityRevoked, store.StateCleanupPending} {
		if _, err := s.r.st.Advance(ctx, "p", s.r.run.Generation, to, "op", "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.r.st.Advance(ctx, "p", s.r.run.Generation, store.StateQuiescent, "op", ""); !errors.Is(err, store.ErrLiveMembers) {
		t.Fatalf("quiescent with an unproven merge process: %v", err)
	}
	s.gone = true
	if out := s.recover(); len(out.StillLive) != 0 || out.Judged != 1 {
		t.Fatalf("proven gone: %+v", out)
	}
	s.want(StateIncomplete, RepairClean)
	if _, err := s.r.st.Advance(ctx, "p", s.r.run.Generation, store.StateQuiescent, "op", ""); err != nil {
		t.Fatalf("quiescent after the proof: %v", err)
	}
}

// Recovery that cannot prove "gone" because the proof itself errors keeps the member live too.
func TestAnErroringProofIsNotGone(t *testing.T) {
	s := repoScene(t)
	s.seed(3, true)
	rec := s.recovery()
	rec.Gone = func(store.Member) (bool, error) { return true, errors.New("cannot tell") }
	out, err := rec.Recover(context.Background(), "", s.r.run.Generation)
	if err != nil || len(out.StillLive) != 1 || len(s.calls) != 0 {
		t.Fatalf("out=%+v err=%v git=%v", out, err, s.calls)
	}
}

// The repair state does not hold the run: a needs-operator operation whose processes are gone is not live, but it
// keeps a second merge out of its repository (T21) until the operator clears it.
func TestANeedsOperatorRepositoryDoesNotHoldTheRunButBlocksMergesUntilCleared(t *testing.T) {
	ctx := context.Background()
	s := repoScene(t)
	s.seed(3, true)
	if err := os.WriteFile(filepath.Join(s.repo, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.recover()
	s.want(StateIncomplete, RepairNeedsOperator)
	if len(s.r.live(t)) != 0 {
		t.Fatal("the needs-operator operation is still a live member")
	}
	dd := s.r.doomsday()
	c := &Conductor{cfg: Config{Doomsday: dd}}
	if _, err := c.beginMerge(ctx, s.repo, "monoagent/t2/w2", "w2"); !errors.Is(err, ErrMergeRepoBusy) {
		t.Fatalf("a merge into a needs-operator repository: %v", err)
	}
	if mo, err := c.beginMerge(ctx, s.repo+"-other", "monoagent/t2/w2", "w2"); err != nil || mo == nil {
		t.Fatalf("another repository was refused: %v", err)
	}
	if err := ClearMerge(ctx, dd.Members, dd.Journal, "p", s.r.run.Generation, "merge-nope", "me", "x"); err == nil {
		t.Error("cleared an operation that is not waiting")
	}
	if err := ClearMerge(ctx, dd.Members, dd.Journal, "p", s.r.run.Generation, s.op, "operator", "resolved by hand"); err != nil {
		t.Fatal(err)
	}
	if mo, err := c.beginMerge(ctx, s.repo, "monoagent/t2/w3", "w3"); err != nil || mo == nil {
		t.Fatalf("a merge after the clear: %v", err)
	}
}

// T24: a journal row removed from the chain. Recovery refuses to act, and so does a new merge.
func TestT24ABrokenChainIsNotActedOn(t *testing.T) {
	s := repoScene(t)
	s.seed(3, true)
	for _, q := range []string{`DROP TRIGGER doomsday_audit_no_delete`, `DELETE FROM doomsday_audit WHERE kind = 'merge.step' AND seq = (SELECT MIN(seq) FROM doomsday_audit WHERE kind = 'merge.step')`} {
		if _, err := s.r.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.recovery().Recover(context.Background(), "", s.r.run.Generation); err == nil {
		t.Fatal("recovery acted on a broken chain")
	}
	if len(s.calls) != 0 || len(s.r.live(t)) != 1 {
		t.Errorf("a broken chain changed something: git %v, live %v", s.calls, s.r.live(t))
	}
	c := &Conductor{cfg: Config{Doomsday: s.r.doomsday()}}
	if _, err := c.beginMerge(context.Background(), s.repo, "monoagent/t2/w2", "w2"); !errors.Is(err, ErrMergeStopped) {
		t.Errorf("a merge began on a broken chain: %v", err)
	}
}
