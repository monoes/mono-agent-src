package dynorg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// Host-death recovery of begun merges (note 13.3 section 5). It runs at the next start of the mode and before
// ReconcileWorktrees (the guard below), it never admits work, and it writes nothing but journal rows and the
// release of a member. Whatever the repair state, a merge member stays live until its processes are proven gone:
// the live member is what blocks quiescence and a new run (the P1 of the first review), and a later pass
// revisits it.

// MergeReconciler is what recovery uses of the store.
type MergeReconciler interface {
	MergeMembers
	LiveMembers(ctx context.Context, profile string, generation int64) ([]store.Member, error)
}

// MergeRecovery is one profile's recovery. Every field but Git is required; a missing one makes Recover fail
// before it changes anything.
type MergeRecovery struct {
	Profile string
	Members MergeReconciler
	Journal MergeJournal
	// Gone proves a member's processes are gone (containment). An error or false leaves the member live.
	Gone func(store.Member) (bool, error)
	// Recorded reports whether the task store holds the merge record of a branch.
	Recorded func(ctx context.Context, branch string) (bool, error)
	// Git runs git for the judgement; nil is the conductor's own hardened runner.
	Git gitFn
	// PlaceFor answers, per pass, the placement of the open run's containment unit: a nil placer (no run open)
	// leaves git as it was; an error stops the pass before any git command (a unit that cannot be proven is not
	// worked in). A nil PlaceFor never places.
	PlaceFor func(ctx context.Context) (GitPlacer, error)
}

// Recovered is what one pass found.
type Recovered struct {
	StillLive     []store.Member // members that are not proven gone: the run is not quiescent
	NeedsOperator []string       // operation ids whose repository was left for the operator
	Judged        int            // operations whose repository was judged in this pass
}

// Recover resolves the operations a dead host left open. repo limits it to one repository ("" is all);
// generations names runs whose live merge members are checked even when they have no journal (an older form).
func (r MergeRecovery) Recover(ctx context.Context, repo string, generations ...int64) (Recovered, error) {
	var out Recovered
	ctx, perr := r.placed(ctx)
	if perr != nil {
		return out, perr
	}
	if r.Members == nil || r.Journal == nil || r.Gone == nil {
		return out, errors.New("merge recovery is not wired; nothing was resolved")
	}
	git := r.Git
	if git == nil {
		git = gitRun
	}
	rows, err := r.Journal.MergeRows(ctx, r.Profile)
	if err != nil {
		return out, err // an unauthenticated journal is not acted on (T24)
	}
	ops, order := foldJournal(rows)
	live, err := r.liveMerges(ctx, ops, generations)
	if err != nil {
		return out, err
	}
	for _, id := range order {
		o := ops[id]
		if o.Open() && (repo == "" || o.rec.Repo == repo) {
			r.resolveOpen(ctx, git, o, live[id], &out)
		}
	}
	for id, m := range live {
		if o := ops[id]; o == nil || o.Ended { // a live member with no open operation: revisit by proof only
			r.revisit(ctx, m, &out)
		}
	}
	for _, id := range order {
		if ops[id].NeedsOperator() && (repo == "" || ops[id].rec.Repo == repo) {
			out.NeedsOperator = append(out.NeedsOperator, id)
		}
	}
	return out, nil
}

// liveMerges lists the live merge members of every generation that has an operation or was named.
func (r MergeRecovery) liveMerges(ctx context.Context, ops map[string]*opView, extra []int64) (map[string]store.Member, error) {
	gens := map[int64]bool{}
	for _, o := range ops {
		gens[o.Gen] = true
	}
	for _, g := range extra {
		gens[g] = true
	}
	live := map[string]store.Member{}
	for g := range gens {
		ms, err := r.Members.LiveMembers(ctx, r.Profile, g)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			if m.Kind == "merge" {
				live[m.ID] = m
			}
		}
	}
	return live, nil
}

// resolveOpen asks the process question first; only a proof of "gone" lets the repository be read.
func (r MergeRecovery) resolveOpen(ctx context.Context, git gitFn, o *opView, m store.Member, out *Recovered) {
	if m.ID == "" {
		m = store.Member{Profile: r.Profile, Generation: o.Gen, ID: o.rec.Op, Kind: "merge"}
	}
	stillLive := m.Live
	if gone, err := r.Gone(m); err != nil || !gone {
		if !o.Unproven {
			_ = r.append(ctx, o.Gen, kindUnproven, mergeRec{Op: o.rec.Op, State: StateIncomplete, Reason: ReasonHostDied, Repair: RepairNeedsOperator,
				Note: "a process of the operation is not proven gone; the repository was not touched"})
		}
		if stillLive {
			out.StillLive = append(out.StillLive, m)
		}
		return
	}
	v := judgeMerge(ctx, git, o, func() (bool, error) {
		if r.Recorded == nil {
			return false, errors.New("the store is not wired")
		}
		return r.Recorded(ctx, o.rec.Branch)
	})
	out.Judged++
	end := mergeRec{Op: o.rec.Op, State: v.State, Reason: ReasonHostDied, Repair: v.Repair, Note: v.Note}
	if v.State == StateFinished {
		end.Reason = ""
	}
	if err := r.append(ctx, o.Gen, kindEnd, end); err != nil {
		if stillLive {
			out.StillLive = append(out.StillLive, m)
		}
		return
	}
	if stillLive {
		if err := r.Members.Release(ctx, r.Profile, o.Gen, m.ID); err != nil {
			out.StillLive = append(out.StillLive, m)
		}
	}
}

// revisit releases a merge member that has no open operation only when containment proves it gone.
func (r MergeRecovery) revisit(ctx context.Context, m store.Member, out *Recovered) {
	if ok, err := r.Gone(m); err != nil || !ok {
		out.StillLive = append(out.StillLive, m)
		return
	}
	if _, err := r.Members.AppendAudit(ctx, r.Profile, m.Generation, "merge.revisit", fmt.Sprintf("%s released: process proven gone", m.ID)); err != nil {
		out.StillLive = append(out.StillLive, m)
		return
	}
	if err := r.Members.Release(ctx, r.Profile, m.Generation, m.ID); err != nil {
		out.StillLive = append(out.StillLive, m)
	}
}

func (r MergeRecovery) append(ctx context.Context, gen int64, kind string, rec mergeRec) error {
	var err error
	for i := 0; i < 8; i++ {
		if _, err = r.Members.AppendAudit(ctx, r.Profile, gen, kind, rec.payload()); err == nil {
			return nil
		}
	}
	return err
}

// RevisitMergeMembers is the older entry: members only, no journal. A live merge member stays live, blocking
// quiescence and a new run, until gone proves its process gone; an error or false answer leaves it live. It
// returns the members still live.
func RevisitMergeMembers(ctx context.Context, st MergeReconciler, profile string, generation int64, gone func(store.Member) (bool, error)) ([]store.Member, error) {
	ms, err := st.LiveMembers(ctx, profile, generation)
	if err != nil {
		return nil, err
	}
	r := MergeRecovery{Members: st, Profile: profile, Gone: gone}
	var out Recovered
	for _, m := range ms {
		if m.Kind == "merge" {
			r.revisit(ctx, m, &out)
		}
	}
	return out.StillLive, nil
}

// BlockedTurns names the turns whose worktrees ReconcileWorktrees must leave: those of operations that are open
// or left for the operator in repo. Branches are monoagent/<turn>/<worker>.
func BlockedTurns(rows []JournalRow, repo string) map[string]bool {
	ops, order := foldJournal(rows)
	out := map[string]bool{}
	for _, id := range order {
		o := ops[id]
		if o.rec.Repo != repo || !o.blocks() {
			continue
		}
		if parts := strings.Split(o.rec.Branch, "/"); len(parts) == 3 && parts[0] == "monoagent" {
			out[parts[1]] = true
		}
	}
	return out
}

// ClearMerge records the operator's report that a repository left as needs-operator is resolved. It refuses any
// other operation. The verb that calls it must be operator-only; this function checks the journal, not the caller.
func ClearMerge(ctx context.Context, m MergeMembers, j MergeJournal, profile string, generation int64, op, by, report string) error {
	rows, err := j.MergeRows(ctx, profile)
	if err != nil {
		return err
	}
	ops, _ := foldJournal(rows)
	o := ops[op]
	if o == nil || !o.NeedsOperator() {
		return fmt.Errorf("operation %q is not waiting for the operator", op)
	}
	rec := mergeRec{Op: op, Note: boundText(strings.TrimSpace("by "+by+": "+report), 300)}
	_, err = m.AppendAudit(ctx, profile, generation, kindCleared, rec.payload())
	return err
}

// reconcileGuard, when set by an engine built with the mode, returns the active-turn test ReconcileWorktrees
// must use for cwd: it runs recovery for cwd's repository first and then protects the turns of operations that
// are not resolved. Nil (the default) leaves reconcile as it was.
var reconcileGuard atomic.Pointer[func(ctx context.Context, cwd string, active func(string) bool) func(string) bool]

// SetReconcileGuard installs (or, with nil, removes) the guard.
func SetReconcileGuard(g func(ctx context.Context, cwd string, active func(string) bool) func(string) bool) {
	if g == nil {
		reconcileGuard.Store(nil)
		return
	}
	reconcileGuard.Store(&g)
}

// GuardTurns is the guard of an engine: recovery before reconcile (note 13.3 section 5.1). A recovery error
// protects every turn of the repository's unresolved operations it could still read; when the journal cannot be
// read at all it protects every turn (fail closed).
func (r MergeRecovery) GuardTurns(ctx context.Context, cwd string, active func(string) bool) func(string) bool {
	ctx, perr := r.placed(ctx)
	if perr != nil {
		return func(string) bool { return true } // fail closed, as an unreadable journal does
	}
	repo := repoOfDir(ctx, r.Git, cwd)
	_, _ = r.Recover(ctx, repo) // a failed pass leaves its operations open, and open operations are protected below
	rows, jerr := r.Journal.MergeRows(ctx, r.Profile)
	if jerr != nil {
		return func(string) bool { return true }
	}
	blocked := BlockedTurns(rows, repo)
	return func(turn string) bool { return blocked[turn] || active(turn) }
}

// repoOfDir resolves a folder to the repository top the journal names.
func repoOfDir(ctx context.Context, git gitFn, dir string) string {
	if git == nil {
		git = gitRun
	}
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return repoTop(&isolation{cwd: strings.TrimSpace(out)})
}
