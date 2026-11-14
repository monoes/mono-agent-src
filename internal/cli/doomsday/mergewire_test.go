package doomsday

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
)

// withDB gives the wiring its database (what the engine file does) and rebuilds the engine from it.
func withDB(w *wireRig, db *storage.Database) {
	w.w.DB = db.DB
	e := w.w.Engine()
	w.rig.deps.Engine = func() Engine { return e }
}

// MergeFor: no run, merges are as they were; a running run, the connection to its gate and journal; a stopped
// one, a gate that admits nothing; no journal or an unreadable run, a refusal.
func TestMergeForFollowsTheProfilesOpenRun(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	ctx := context.Background()
	g := &HostGate{Store: w.w.Chained(), Fresh: w.w.Fresh(), Profile: "p1", RunDir: w.w.RunDir, Kernel: w.w.Kernel, Journal: MergeJournal(db.DB, w.w.Store)}
	if dd, err := g.MergeFor(ctx); dd != nil || err != nil {
		t.Fatalf("no run: %v, %v", dd, err)
	}
	w.start(t)
	dd, err := g.MergeFor(ctx)
	if err != nil || dd == nil || dd.Generation != 1 || dd.Profile != "p1" || dd.Journal == nil {
		t.Fatalf("running: %+v, %v", dd, err)
	}
	if again, _ := g.MergeFor(ctx); again != dd {
		t.Error("the connection is rebuilt on every merge, so its ids are not unique")
	}
	if _, err := dd.Gate.Admit(stop.Spec{Kind: stop.KindE1}); err != nil {
		t.Errorf("a running run refuses a merge: %v", err)
	}
	if _, err := run(t, w.deps, "", "stop"); err != nil {
		t.Fatal(err)
	}
	if dd, err := g.MergeFor(ctx); err == nil && dd != nil {
		if _, aerr := dd.Gate.Admit(stop.Spec{Kind: stop.KindE1}); aerr == nil {
			t.Error("a stopped run admits a merge")
		}
	}
	w2, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w2.start(t)
	nojournal := &HostGate{Store: w2.w.Chained(), Fresh: w2.w.Fresh(), Profile: "p1", RunDir: w2.w.RunDir, Kernel: w2.w.Kernel}
	if dd, err := nojournal.MergeFor(ctx); dd != nil || err == nil {
		t.Errorf("no journal: %v, %v", dd, err)
	}
}

// The start path and the stop path revisit a merge member a dead host left (note 13.3 P1): it stays live, and so
// the run open, until the containment proof says its processes are gone; then it is released and the run stops.
func TestStopAndStartRevisitAMergeMemberUntilContainmentProvesItGone(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	withDB(w, db)
	ctx := context.Background()
	w.start(t)
	m := store.Member{Profile: "p1", Generation: 1, ID: "merge-dead-1", Kind: "merge", Pid: deadPID(t), Stamp: "host"}
	if err := w.w.Store.Register(ctx, m); err != nil {
		t.Fatal(err)
	}

	// a live process in the unit besides its anchor: not proven gone, so stop does not say "stopped" and the
	// member is still live (the unit is ended after the revisit, so the next stop finds nothing)
	w.world.extra = true
	out, _ := run(t, w.deps, "", "stop")
	if strings.HasSuffix(out, "stopped\n") {
		t.Fatalf("stopped with a merge member whose processes are not proven gone: %q", out)
	}
	if ms, _ := w.w.Store.LiveMembers(ctx, "p1", 1); len(ms) != 1 {
		t.Fatalf("members = %v, want the merge member still live", ms)
	}
	// start is refused while the run is open, and says why it is open
	h := fullHash(t, w.rig, w.charter)
	if _, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h); err == nil {
		t.Fatal("a second run started while the first has a live merge member")
	}

	// the first stop ended the unit's service and its cgroup is absent: the processes are proven gone
	out, err := run(t, w.deps, "", "stop")
	if err != nil || !strings.HasSuffix(out, "stopped\n") {
		t.Fatalf("stop after the proof: %v %q", err, out)
	}
	if ms, _ := w.w.Store.LiveMembers(ctx, "p1", 1); len(ms) != 0 {
		t.Errorf("members = %v, want none", ms)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM doomsday_audit WHERE kind = 'merge.revisit'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("revisit audit rows = %d (%v), want 1", n, err)
	}
}

// A start whose revisit cannot even read the journal refuses (fail closed) and enters nothing.
func TestStartRefusesWhenTheMergeRevisitFails(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	withDB(w, db)
	w.w.Store = store.New(db.DB, failingKeyer{}) // every chain check fails
	e := w.w.Engine()
	w.rig.deps.Engine = func() Engine { return e }
	if _, err := db.DB.Exec(`INSERT INTO doomsday_audit(profile, generation, seq, kind, payload, prev, mac) VALUES ('p1', 9, 1, 'merge.begin', '{"op":"x"}', x'', x'00')`); err != nil {
		t.Fatal(err)
	}
	h := fullHash(t, w.rig, w.charter)
	if _, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h); err == nil || !strings.Contains(err.Error(), "revisited") {
		t.Fatalf("start = %v", err)
	}
	var n int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM doomsday_runs`).Scan(&n); err != nil || n != 0 {
		t.Errorf("runs = %d (%v), want none", n, err)
	}
}

type failingKeyer struct{}

func (failingKeyer) MAC(string, ...[]byte) ([]byte, error) { return nil, errors.New("no key") }

type standaloneOnly struct {
	stop.Placement
	placed, ticketed int
}

func (s *standaloneOnly) PlaceStandalone(cmd *exec.Cmd) (func(), error) {
	s.placed++
	return func() {}, nil
}
func (s *standaloneOnly) Place(*exec.Cmd) (func(), error) {
	return nil, errors.New("turn placement used for git")
}
func (s *standaloneOnly) PlaceTicket(uint64, *exec.Cmd) (func(), error) {
	return nil, errors.New("ticket placement used for git")
}

// A unit that takes a command only as a recorded session leader (macOS) is given a merge's git command through its standalone
// entry, with or without a ticket; any other unit keeps Place and PlaceTicket.
func TestGitPlacementUsesTheStandaloneEntryOfAUnitThatHasOne(t *testing.T) {
	s := &standaloneOnly{}
	if _, err := gitPlace(s)(exec.Command("git")); err != nil {
		t.Fatal(err)
	}
	if _, err := gitPlaceTicket(s)(3, exec.Command("git")); err != nil {
		t.Fatal(err)
	}
	if s.placed != 2 {
		t.Fatalf("standalone placements: %d", s.placed)
	}
	plain := &plainPlacement{}
	_, _ = gitPlace(plain)(exec.Command("git"))
	_, _ = gitPlaceTicket(plain)(3, exec.Command("git"))
	if plain.place != 1 || plain.ticket != 1 {
		t.Fatalf("a unit with no standalone entry: %+v", plain)
	}
}

type plainPlacement struct{ place, ticket int }

func (p *plainPlacement) Place(*exec.Cmd) (func(), error) { p.place++; return func() {}, nil }
func (p *plainPlacement) PlaceTicket(uint64, *exec.Cmd) (func(), error) {
	p.ticket++
	return func() {}, nil
}
func (p *plainPlacement) PlaceLeased(*exec.Cmd) error { return nil }
