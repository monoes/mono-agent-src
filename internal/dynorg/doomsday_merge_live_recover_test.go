package dynorg

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// P1 (final review): recovery runs outside host start (every isolated turn start, the Stop verb), and a live
// merge between two steps has an empty unit. Recovery may end a merge only with all three proofs: it holds the
// host lock itself, the member's pid is not running, and containment says gone.
func TestRecoveryNeverEndsALiveMerge(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(*MergeRecovery){
		"the member's process is running (the unit looks empty between two steps)": func(r *MergeRecovery) { r.Alive = func(int) bool { return true } },
		"another host holds the host lock":                                         func(r *MergeRecovery) { r.HostLock = func() (func(), error) { return nil, errors.New("second host") } },
		"no host lock is wired":                                                    func(r *MergeRecovery) { r.HostLock = nil },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := repoScene(t)
			s.gone = true // the unit is empty at this instant
			s.seed(2, true)
			rec := s.recovery()
			mutate(&rec)
			out, err := rec.Recover(ctx, "", s.r.run.Generation)
			if err != nil {
				t.Fatal(err)
			}
			if o := s.view(); !o.Open() {
				t.Fatalf("a live merge was ended: %+v", o.End)
			}
			if len(out.StillLive) != 1 || out.Judged != 0 || len(s.calls) != 0 {
				t.Fatalf("still live %v, judged %d, git calls %v", out.StillLive, out.Judged, s.calls)
			}
			if len(s.r.live(t)) != 1 {
				t.Fatal("the member of a live merge was released")
			}
			// the same through the guard every isolated turn start runs
			rec.GuardTurns(ctx, s.repo, func(string) bool { return false })
			if o := s.view(); !o.Open() || len(s.r.live(t)) != 1 {
				t.Fatal("GuardTurns ended a live merge")
			}
		})
	}
	t.Run("a member with no open operation is not released while its process runs", func(t *testing.T) {
		r := newDDRig(t)
		if err := r.st.Register(ctx, store.Member{Profile: "p", Generation: r.run.Generation, ID: "merge-live-1", Kind: "merge", Pid: os.Getpid(), Stamp: "host"}); err != nil {
			t.Fatal(err)
		}
		rec := MergeRecovery{Profile: "p", Members: r.st, Journal: r.journal(),
			Gone: func(store.Member) (bool, error) { return true, nil }, HostLock: okLock, Alive: func(int) bool { return true }}
		if out, err := rec.Recover(ctx, "", r.run.Generation); err != nil || len(out.StillLive) != 1 {
			t.Fatalf("%v %v", out, err)
		}
	})
	t.Run("the proofs together do end a dead merge", func(t *testing.T) {
		s := repoScene(t)
		s.seed(2, true)
		s.recover()
		if o := s.view(); !o.Ended {
			t.Fatal("a proven-dead merge stayed open")
		}
	})
}

// Journal fold: the first end row of an operation is its end; a later one (a live host finishing after a wrong
// judgement) never rewrites it.
func TestFoldJournalFirstEndRowWins(t *testing.T) {
	row := func(seq int64, kind string, rec mergeRec) JournalRow {
		return JournalRow{Generation: 1, Seq: seq, Kind: kind, Rec: rec}
	}
	rows := []JournalRow{
		row(1, kindBegin, mergeRec{Op: "m1", Repo: "/r", Branch: "monoagent/t/w"}),
		row(2, kindEnd, mergeRec{Op: "m1", State: StateIncomplete, Reason: ReasonHostDied, Repair: RepairClean}),
		row(3, kindEnd, mergeRec{Op: "m1", State: StateFinished}),
	}
	ops, _ := foldJournal(rows)
	if o := ops["m1"]; !o.Ended || o.End.State != StateIncomplete || o.End.Repair != RepairClean {
		t.Fatalf("end = %+v, the first end row must win", o.End)
	}
}

// A turn cancelled while the claim is being recorded (Stop is that moment) must still release its member.
func TestAFailedBeginReleasesTheMemberEvenWhenTheTurnIsCancelled(t *testing.T) {
	r := newDDRig(t)
	c, _ := ddWriter(t, r, "d9")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.st.failKind, r.st.onFail = kindBegin, cancel
	if _, err := c.Merge(ctx, "w1"); err == nil {
		t.Fatal("the merge went on without a recorded claim")
	}
	if live := r.live(t); len(live) != 0 {
		t.Fatalf("the member stayed live after a cancelled turn: %v", live)
	}
}
