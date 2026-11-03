//go:build linux

package stop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
)

type seqRig struct {
	seq *Sequencer
	st  *store.SQLStore
	fs  *faultFS
}

// newSeqRig is the sequencer over a real migrated SQLite file with a pinned connection, a real witness in a scratch
// run directory (faultFS lets a test fail the appends), and a file secret store for the anchor.
func newSeqRig(t *testing.T) *seqRig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dd.db")
	db, err := storage.NewDatabase(path)
	mustNoErr(t, err)
	mustNoErr(t, db.ApplyMigrations())
	t.Cleanup(func() { db.DB.Close() })
	p, err := Pin(bg, PinOptions{DBPath: path})
	mustNoErr(t, err)
	t.Cleanup(func() { p.mu.Lock(); p.discardLocked(); p.mu.Unlock() })
	fs := newFaultFS()
	dir := newRunDir(t, fs)
	keys := testKeyer()
	w := Witness{Dir: dir, Keys: keys}
	mustNoErr(t, w.Init())
	mustNoErr(t, w.Append(Record{T: RecDBID, Extra: p.Identity().String()}))
	mustNoErr(t, w.Append(Record{T: RecHeader, Gen: 1}))
	return &seqRig{
		seq: &Sequencer{W: w, Pin: p, Counter: GenCounter{Dir: dir, Keys: keys}, Profile: "p",
			Anchor: &Anchor{Store: &FileSecretStore{Dir: t.TempDir()}, Keys: keys, Profile: "p"}},
		st: store.New(db.DB, keys), fs: fs,
	}
}

func (r *seqRig) write(kind string) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := r.st.AppendAudit(ctx, "p", 1, kind, "x")
		return err
	}
}

func (r *seqRig) types(t *testing.T) []string {
	t.Helper()
	recs, err := r.seq.W.Records()
	mustNoErr(t, err)
	var out []string
	for _, x := range recs {
		out = append(out, x.T)
	}
	return out
}

func TestSequenceWritesPendingThenCommitted(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.Sequence(bg, 1, "audit", false, false, r.write("a")))
	mustNoErr(t, r.seq.Sequence(bg, 1, "audit", false, false, r.write("b")))
	got := r.types(t)
	want := []string{RecDBID, RecHeader, RecPending, RecCommitted, RecPending, RecCommitted}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v", got)
		}
	}
	mustNoErr(t, r.seq.Check(bg, 1))
}

// Crash after the database commit and before the committed line: the next check repairs the witness (row 3).
func TestCrashAfterTheDatabaseCommitIsRepairedAsRow3(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.Sequence(bg, 1, "audit", false, false, r.write("a")))
	err := r.seq.Sequence(bg, 1, "audit", false, false, func(ctx context.Context) error {
		e := r.write("b")(ctx)
		r.fs.failAppend = true // the committed line cannot be written: the process "dies" here
		return e
	})
	r.fs.failAppend = false
	mustNoErr(t, err) // the transition itself succeeded
	if got := r.types(t); got[len(got)-1] != RecPending {
		t.Fatalf("the committed line was written despite the failure: %v", got)
	}
	mustNoErr(t, r.seq.Check(bg, 1))
	if got := r.types(t); got[len(got)-1] != RecCommitted {
		t.Fatalf("not repaired: %v", got)
	}
	mustNoErr(t, r.seq.Sequence(bg, 1, "audit", false, false, r.write("c")))
}

// Crash before the database commit: the pending line stays; the next check abandons it (row 2) and the stream goes on.
func TestCrashBeforeTheDatabaseCommitIsAbandonedAsRow2(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.Sequence(bg, 1, "audit", false, false, r.write("a")))
	boom := errors.New("died")
	err := r.seq.Sequence(bg, 1, "audit", false, false, func(context.Context) error {
		r.fs.failAppend = true // nothing was written, and the abandoned line cannot be either
		return boom
	})
	r.fs.failAppend = false
	if !errors.Is(err, boom) {
		t.Fatalf("%v", err)
	}
	if got := r.types(t); got[len(got)-1] != RecPending {
		t.Fatalf("%v", got)
	}
	mustNoErr(t, r.seq.Check(bg, 1))
	if got := r.types(t); got[len(got)-1] != RecAbandoned {
		t.Fatalf("not abandoned: %v", got)
	}
	// the same range is used again, and the stream is not read as damaged (an abandoned line goes back a seq)
	mustNoErr(t, r.seq.Sequence(bg, 1, "audit", false, false, r.write("b")))
	mustNoErr(t, r.seq.Check(bg, 1))
}

// A transition that was ending when the host died is not repaired: it fails closed (row 2e) and refuses afterwards.
func TestCrashDuringAnEndingTransitionFailsClosed(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.Sequence(bg, 1, "audit", false, false, r.write("a")))
	_ = r.seq.Sequence(bg, 1, "advance", true, false, func(context.Context) error {
		r.fs.failAppend = true
		return errors.New("died")
	})
	r.fs.failAppend = false
	var fe *FreshnessError
	if err := r.seq.Check(bg, 1); !errors.As(err, &fe) || fe.Row != 2 || fe.Hold {
		t.Fatalf("%v", err)
	}
	if !r.seq.W.Dir.IsRefused(1) {
		t.Fatal("no durable refusal")
	}
	if err := r.seq.Sequence(bg, 1, "audit", false, false, r.write("b")); err == nil {
		t.Fatal("a refused generation took another transition")
	}
}

// Record-then-act: when the pending line cannot be written, the database is not touched.
func TestNoPendingLineNoTransition(t *testing.T) {
	r := newSeqRig(t)
	r.fs.failAppend = true
	called := false
	err := r.seq.Sequence(bg, 1, "audit", false, false, func(context.Context) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("the transition ran without a pending line: %v called=%v", err, called)
	}
}

// Sequence numbers are per generation: the second generation's first lines are not older than the first's last.
func TestSeqsArePerGeneration(t *testing.T) {
	r := newSeqRig(t)
	for i := 0; i < 3; i++ {
		mustNoErr(t, r.seq.Sequence(bg, 1, "audit", false, false, r.write("a")))
	}
	mustNoErr(t, r.seq.W.Append(Record{T: RecHeader, Gen: 2}))
	mustNoErr(t, r.seq.W.Append(Record{T: RecPending, Gen: 2, From: 1, To: 1}))
	if _, err := r.seq.W.Records(); err != nil {
		t.Fatalf("a new generation's first line was read as damage: %v", err)
	}
}

// Q-c of note 13.2: a unit that is gone but whose end line cannot be written is not proven; one that is written once.
func TestUnitEndedLineIsRequiredAndWrittenOnce(t *testing.T) {
	r := newSeqRig(t)
	rec := testRecord()
	rec.Generation = 1
	mustNoErr(t, r.seq.Bound(rec))
	e := NewWitnessEnder(&fakeEnder{}, r.seq)
	r.fs.failAppend = true
	if out := e.End(bg, 1, nil); out.Kind != UnitNotProven {
		t.Fatalf("a unit whose end line could not be written was reported ended: %+v", out)
	}
	r.fs.failAppend = false
	if out := e.End(bg, 1, nil); out.Kind != UnitEnded {
		t.Fatalf("%+v", out)
	}
	e.End(bg, 1, nil)
	n := 0
	for _, s := range r.types(t) {
		if s == RecUnitEnded {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d unit-ended lines", n)
	}
	if got := r.seq.BoundUnits(1); len(got) != 1 || got[0] != rec {
		t.Fatalf("the bound record does not round-trip: %+v", got)
	}
	// begun operations still running: the unit is not over, no end line
	r2 := newSeqRig(t)
	mustNoErr(t, r2.seq.Bound(rec))
	NewWitnessEnder(&fakeEnder{}, r2.seq).End(bg, 1, []uint64{7})
	for _, s := range r2.types(t) {
		if s == RecUnitEnded {
			t.Fatal("unit-ended written while a begun operation still ran")
		}
	}
}

// Entry rules: a deleted witness, a damaged counter and a database behind the counter all refuse.
func TestBeginEntryRefusals(t *testing.T) {
	t.Run("witness deleted after a first run", func(t *testing.T) {
		r := newSeqRig(t)
		mustNoErr(t, os.Remove(r.seq.W.path()))
		if err := r.seq.BeginEntry(bg, 2); !errors.Is(err, ErrEntryRefused) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("counter damaged", func(t *testing.T) {
		r := newSeqRig(t)
		_, err := r.seq.Counter.Reserve(0, 0, 0)
		mustNoErr(t, err)
		b, _ := os.ReadFile(r.seq.Counter.path())
		b[3] ^= 0xff
		mustNoErr(t, os.WriteFile(r.seq.Counter.path(), b, 0o600))
		if err := r.seq.BeginEntry(bg, 2); !errors.Is(err, ErrEntryRefused) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("database behind the counter", func(t *testing.T) {
		r := newSeqRig(t)
		for i := 0; i < 3; i++ {
			_, err := r.seq.Counter.Reserve(int64(i), 0, 0)
			mustNoErr(t, err)
		}
		if err := r.seq.BeginEntry(bg, 2); !errors.Is(err, ErrEntryRefused) {
			t.Fatalf("a database that offers generation 2 after the counter reached 3: %v", err)
		}
	})
}

// The head is read only through the pinned connection: a swapped database file is refused at the next check.
func TestSwappedDatabaseFileFailsTheCheck(t *testing.T) {
	r := newSeqRig(t)
	path := r.seq.Pin.paths[0]
	mustNoErr(t, os.Rename(path, path+".moved"))
	mustNoErr(t, os.WriteFile(path, nil, 0o600))
	var fe *FreshnessError
	if err := r.seq.Check(bg, 1); !errors.As(err, &fe) {
		t.Fatalf("a swapped database file was admitted: %v", err)
	}
}
