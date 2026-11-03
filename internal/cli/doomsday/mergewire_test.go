package doomsday

import (
	"context"
	"errors"
	"fmt"
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

func (w *wireRig) unitRecord(t *testing.T, gen uint64, boot string) {
	t.Helper()
	const nonce = "0123456789abcdef0123456789abcdef"
	name := fmt.Sprintf("monoagent-run-%d-%s.service", gen, nonce)
	rec := stop.UnitRecord{Kind: "cgroup2", BootID: boot, Name: name, CgroupPath: "/sys/fs/cgroup/app/" + name, CgroupID: 1, OwnerUID: 1, Nonce: nonce, Generation: gen}
	if err := w.w.RunDir.WriteUnitFile(rec); err != nil {
		t.Fatal(err)
	}
}

// MergeFor: no run, merges are as they were; a running run, the connection to its gate and journal; a stopped
// one, a gate that admits nothing; no journal or an unreadable run, a refusal.
func TestMergeForFollowsTheProfilesOpenRun(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	ctx := context.Background()
	g := &HostGate{Store: w.w.Store, Profile: "p1", RunDir: w.w.RunDir, Journal: MergeJournal(db.DB, w.w.Store)}
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
	nojournal := &HostGate{Store: w2.w.Store, Profile: "p1", RunDir: w2.w.RunDir}
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
	m := store.Member{Profile: "p1", Generation: 1, ID: "merge-dead-1", Kind: "merge", Pid: 4242, Stamp: "host"}
	if err := w.w.Store.Register(ctx, m); err != nil {
		t.Fatal(err)
	}

	// no recorded unit: not proven, so stop does not say "stopped" and the member is still live
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

	// the unit is recorded for an earlier boot: a reboot is the proof every process is gone
	w.unitRecord(t, 1, "an-earlier-boot")
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
