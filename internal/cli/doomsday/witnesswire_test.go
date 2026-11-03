package doomsday

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// These tests run the witness of note 13.5 as production wires it: the real SQLite store, the real witness, counter
// and refusal files in a scratch run directory, a pinned connection on the database file, and the anchor in a file
// secret store (the sandbox has no OS secret store; OSSecretStore itself is not exercised here).

func witnessOf(w *wireRig) stop.Witness {
	return stop.Witness{Dir: w.w.RunDir, Keys: w.w.Witness.Keys}
}

func anchorOf(w *wireRig) *stop.Anchor {
	return &stop.Anchor{Store: w.w.Witness.Secrets, Keys: w.w.Witness.Keys, Profile: w.w.Profile}
}

func recTypes(t *testing.T, w *wireRig) []string {
	t.Helper()
	recs, err := witnessOf(w).Records()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range recs {
		out = append(out, r.T)
	}
	return out
}

func indexOf(ts []string, want string) int {
	for i, s := range ts {
		if s == want {
			return i
		}
	}
	return -1
}

func rewire(w *wireRig) {
	e := w.w.Engine()
	w.deps.Engine = func() Engine { return e }
}

func newHostRigWith(t *testing.T) *hostRig {
	t.Helper()
	r := newHostRig(t)
	r.start(t)
	if err := r.startTurn(); err != nil {
		t.Fatalf("a turn on a fresh run was refused: %v", err)
	}
	return r
}

func auditRows(t *testing.T, r *hostRig) int {
	t.Helper()
	return count(t, r.db, `SELECT COUNT(*) FROM doomsday_audit`)
}

// The lines of a whole run: dbid, then the unit's intent and binding before the generation's own lines, the anchor, and at
// Stop the unit's end; every pending line is followed by its committed line.
func TestStartAndStopWriteTheWitnessLinesInOrder(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	var intentSeenAtStart bool
	w.world.onStart = func(name string) {
		recs, _ := witnessOf(w).Records()
		for _, r := range recs {
			intentSeenAtStart = intentSeenAtStart || (r.T == stop.RecUnitIntent && strings.Contains(r.Extra, "name="+name))
		}
	}
	out := w.start(t)
	if !intentSeenAtStart {
		t.Fatal("the service was asked for before unit-intent was in the witness")
	}
	if !strings.Contains(out, "anchor-bound: yes") {
		t.Fatalf("the entry does not say the run is anchor-bound: %q", out)
	}
	ts := recTypes(t, w)
	if ts[0] != stop.RecDBID || !(indexOf(ts, stop.RecUnitIntent) < indexOf(ts, stop.RecUnitBound) && indexOf(ts, stop.RecUnitBound) < indexOf(ts, stop.RecHeader)) {
		t.Fatalf("lines out of order: %v", ts)
	}
	if indexOf(ts, stop.RecAnchor) < 0 || indexOf(ts, stop.RecUnitEnded) >= 0 {
		t.Fatalf("anchor line missing or unit-ended written at start: %v", ts)
	}
	pend, comm := 0, 0
	for _, s := range ts {
		if s == stop.RecPending {
			pend++
		}
		if s == stop.RecCommitted {
			comm++
		}
	}
	if pend == 0 || pend != comm {
		t.Fatalf("%d pending, %d committed: %v", pend, comm, ts)
	}
	if max, err := (stop.GenCounter{Dir: w.w.RunDir, Keys: w.w.Witness.Keys}).Max(); err != nil || max != 1 {
		t.Fatalf("generation counter %d, %v", max, err)
	}
	if st, err := anchorOf(w).Read(); err != nil || !st.Found || st.Generation != 1 || st.Ended {
		t.Fatalf("anchor after start: %+v %v", st, err)
	}

	if out, err := run(t, w.deps, "", "stop"); err != nil || !strings.HasSuffix(out, "stopped\n") {
		t.Fatalf("stop: %v %q", err, out)
	}
	ts = recTypes(t, w)
	if indexOf(ts, stop.RecUnitEnded) < 0 {
		t.Fatalf("no unit-ended line after a proven end: %v", ts)
	}
	if st, _ := anchorOf(w).Read(); !st.Ended {
		t.Fatalf("anchor not ended after stop: %+v", st)
	}
	recs, _ := witnessOf(w).Records()
	if last := recs[len(recs)-1]; last.T != stop.RecUnitEnded && !(last.T == stop.RecCommitted && last.Ended) {
		t.Fatalf("the generation's end is not witnessed at the tail: %+v", last)
	}
	// a second run: generation 2, counter 2, anchor 2
	w.start(t)
	if max, _ := (stop.GenCounter{Dir: w.w.RunDir, Keys: w.w.Witness.Keys}).Max(); max != 2 {
		t.Fatalf("counter after the second run: %d", max)
	}
	if st, _ := anchorOf(w).Read(); st.Generation != 2 {
		t.Fatalf("anchor after the second run: %+v", st)
	}
}

// No witness wired, no run: the mode refuses and writes nothing.
func TestNoWitnessSetupRefusesToStart(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	h := fullHash(t, w.rig, w.charter)
	w.w.Witness = nil
	rewire(w)
	if out, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h); err == nil || !strings.Contains(err.Error(), "no witness") {
		t.Fatalf("started with no witness: %v %q", err, out)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 0 || len(w.world.started) != 0 {
		t.Fatalf("%d run row(s), %d unit(s) from a refused start", n, len(w.world.started))
	}
}

// Where no secret store exists the run starts anchor-bound: no, says so, and admissions carry on without an anchor.
func TestNoSecretStoreStartsUnanchoredAndSaysSo(t *testing.T) {
	for name, s := range map[string]stop.SecretStore{"none": nil, "refusing": stop.NoSecretStore{}} {
		t.Run(name, func(t *testing.T) {
			r := newHostRig(t)
			r.w.Witness.Secrets = s
			rewire(r.wireRig)
			r.gate.Fresh = r.w.Fresh()
			out := r.start(t)
			if !strings.Contains(out, "anchor-bound: no") || !strings.Contains(out, "rollback detection is weaker") {
				t.Fatalf("the entry hides that there is no anchor: %q", out)
			}
			if indexOf(recTypes(t, r.wireRig), stop.RecAnchor) >= 0 {
				t.Fatal("an anchor line for a run that has none")
			}
			if err := r.startTurn(); err != nil {
				t.Fatalf("an unanchored run refused a turn: %v", err)
			}
		})
	}
}

// Rows 9, 10 and 11 at every admission: a newer anchor, an unreadable one and a missing one.
func TestAdmissionAnchorRows(t *testing.T) {
	t.Run("row 9 anchor newer than the database", func(t *testing.T) {
		r := newHostRigWith(t)
		if err := anchorOf(r.wireRig).Set(2, stop.AnchorRunning); err != nil {
			t.Fatal(err)
		}
		if err := r.startTurn(); !refused(err) || !strings.Contains(err.Error(), "row 9") {
			t.Fatalf("a turn was admitted against a newer anchor: %v", err)
		}
	})
	t.Run("row 10 locked store holds without sealing", func(t *testing.T) {
		r := newHostRigWith(t)
		fs := r.w.Witness.Secrets.(*stop.FileSecretStore)
		fs.Locked = true
		if err := r.startTurn(); !refused(err) || !strings.Contains(err.Error(), "row 10") {
			t.Fatalf("a turn was admitted with the anchor unreadable: %v", err)
		}
		fs.Locked = false
		if err := r.startTurn(); err != nil {
			t.Fatalf("a hold must not seal the generation: %v", err)
		}
	})
	t.Run("row 11 missing item seals", func(t *testing.T) {
		r := newHostRigWith(t)
		if err := os.Remove(filepath.Join(r.w.Witness.Secrets.(*stop.FileSecretStore).Dir, "anchor-p1")); err != nil {
			t.Fatal(err)
		}
		n := auditRows(t, r)
		if err := r.startTurn(); !refused(err) || !strings.Contains(err.Error(), "row 11") {
			t.Fatalf("a turn was admitted with the anchor item missing: %v", err)
		}
		if auditRows(t, r) != n {
			t.Fatal("a refused admission wrote a chained row")
		}
		if err := anchorOf(r.wireRig).Set(1, stop.AnchorRunning); err != nil { // put it back: the refusal is durable
			t.Fatal(err)
		}
		if err := r.startTurn(); !refused(err) {
			t.Fatalf("the generation recovered after a fail-closed row: %v", err)
		}
	})
}

// A restored older database (truncated audit tail) is caught by the witness at the next admission.
func TestTruncatedAuditTailIsCaughtAtAdmission(t *testing.T) {
	r := newHostRigWith(t)
	if _, err := r.db.DB.Exec(`DROP TRIGGER doomsday_audit_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.DB.Exec(`DELETE FROM doomsday_audit WHERE seq = (SELECT MAX(seq) FROM doomsday_audit)`); err != nil {
		t.Fatal(err)
	}
	if err := r.startTurn(); !refused(err) || !strings.Contains(err.Error(), "row 4") {
		t.Fatalf("a truncated audit tail was admitted: %v", err)
	}
}

// A row written by something that did not go through the sequencer leaves the database ahead of the witness.
func TestUnsequencedWriteIsCaught(t *testing.T) {
	r := newHostRigWith(t)
	if _, err := r.w.Store.AppendAudit(context.Background(), "p1", 1, "x", "y"); err != nil { // the raw store, not Chained()
		t.Fatal(err)
	}
	if err := r.startTurn(); !refused(err) || !strings.Contains(err.Error(), "row 5") {
		t.Fatalf("an unwitnessed row was admitted: %v", err)
	}
}

// A missing or damaged witness fails closed at admission, and Stop still works.
func TestMissingOrDamagedWitnessFailsClosed(t *testing.T) {
	cases := map[string]func(path string) error{
		"missing": os.Remove,
		"damaged": func(p string) error {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			b[len(b)/2] ^= 0xff
			return os.WriteFile(p, b, 0o600)
		},
	}
	for name, hurt := range cases {
		t.Run(name, func(t *testing.T) {
			r := newHostRigWith(t)
			if err := hurt(filepath.Join(r.w.RunDir.Dir, "witness")); err != nil {
				t.Fatal(err)
			}
			if err := r.startTurn(); !refused(err) || !strings.Contains(err.Error(), "row 7") {
				t.Fatalf("a turn was admitted with a %s witness: %v", name, err)
			}
			out, err := run(t, r.deps, "", "stop")
			if !strings.Contains(out, "stop marker") && !strings.Contains(out, "NOT stopped") && err == nil {
				t.Fatalf("stop with a %s witness neither stopped nor said why: %q", name, out)
			}
			if _, serr := os.Stat(r.w.RunDir.StopPath(1)); serr != nil {
				t.Fatalf("no stop marker with a %s witness: %v", name, serr)
			}
		})
	}
}

// The refusal file outlives a torn "failed" record: truncating the witness tail does not reopen the generation.
func TestTornFailedRecordStillRefuses(t *testing.T) {
	r := newHostRigWith(t)
	path := filepath.Join(r.w.RunDir.Dir, "witness")
	if err := anchorOf(r.wireRig).Set(2, stop.AnchorRunning); err != nil {
		t.Fatal(err)
	}
	if err := r.startTurn(); !refused(err) {
		t.Fatalf("setup: %v", err)
	}
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, b[:len(b)-5], 0o600); err != nil { // the failed record is torn
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(r.w.Witness.Secrets.(*stop.FileSecretStore).Dir, "anchor-p1")); err != nil {
		t.Fatal(err)
	}
	if err := anchorOf(r.wireRig).Set(1, stop.AnchorRunning); err != nil { // the anchor now agrees again
		t.Fatal(err)
	}
	g := &HostGate{Store: r.w.Chained(), Fresh: r.w.Fresh(), Profile: "p1", RunDir: r.w.RunDir, BindingOn: func() (bool, error) { return true, nil }}
	if err := g.Fresh(context.Background(), 1); err == nil || !strings.Contains(err.Error(), "row 13") {
		t.Fatalf("a torn failure record reopened the generation: %v", err)
	}
}

// The entry rule: a database that offers a generation the outside memories have already seen is refused.
func TestEntryRefusesADatabaseBehindTheOutsideMemory(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	if err := anchorOf(w).Set(5, stop.AnchorEnded); err != nil {
		t.Fatal(err)
	}
	h := fullHash(t, w.rig, w.charter)
	_, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("started on a database behind the anchor: %v", err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM doomsday_runs`); n != 0 || len(w.world.started) != 0 {
		t.Fatalf("%d run(s), %d unit(s) after a refused entry", n, len(w.world.started))
	}
}

func TestEntryWithALockedAnchorStoreHolds(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.Witness.Secrets.(*stop.FileSecretStore).Locked = true
	h := fullHash(t, w.rig, w.charter)
	_, err := run(t, w.deps, "", "start", "--charter", w.charter, "--yes-i-lift", "--hash", h)
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("started with the anchor store locked: %v", err)
	}
	var fe *stop.FreshnessError
	if !errors.As(err, &fe) || !fe.Hold {
		t.Fatalf("not a hold: %v", err)
	}
}

// The store shows the profile's run state through the pinned path only: a sequencer without a pin refuses everything.
func TestSequencerWithoutAPinRefuses(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	seq := &stop.Sequencer{W: witnessOf(w), Counter: stop.GenCounter{Dir: w.w.RunDir, Keys: w.w.Witness.Keys}, Profile: "p1"}
	s := &SeqStore{SQLStore: w.w.Store, Seq: seq}
	if _, err := s.AppendAudit(context.Background(), "p1", 1, "k", "v"); err == nil {
		t.Fatal("a chained write without a pinned connection")
	}
}

// The store's own admission and registration check freshness first (merge members register through them).
func TestStoreAdmitAndRegisterCheckFreshness(t *testing.T) {
	r := newHostRigWith(t)
	st := r.w.Chained()
	ctx := context.Background()
	if err := st.Admit(ctx, r.db.DB, "p1", 1); err != nil {
		t.Fatalf("a fresh run was not admitted: %v", err)
	}
	if err := anchorOf(r.wireRig).Set(2, stop.AnchorRunning); err != nil {
		t.Fatal(err)
	}
	var ref *store.Refusal
	if err := st.Admit(ctx, r.db.DB, "p1", 1); !errors.As(err, &ref) || !strings.Contains(err.Error(), "row 9") {
		t.Fatalf("a stale run was admitted: %v", err)
	}
	m := store.Member{Profile: "p1", Generation: 1, ID: "m", Kind: "pid", Pid: 5, Stamp: "1", Live: true}
	if err := st.Register(ctx, m); !errors.As(err, &ref) {
		t.Fatalf("a member registered on a stale run: %v", err)
	}
}
