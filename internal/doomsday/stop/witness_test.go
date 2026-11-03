package stop

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
)

func testKeyer() store.Keyer {
	return VaultKeyer{Key: func() ([]byte, error) { return []byte("test-vault-key"), nil }}
}

func newWitness(t *testing.T, fsys FS) Witness {
	t.Helper()
	w := Witness{Dir: newRunDir(t, fsys), Keys: testKeyer()}
	mustNoErr(t, w.Init())
	return w
}

func TestWitnessChainAppendAndRead(t *testing.T) {
	w := newWitness(t, nil)
	mustNoErr(t, w.Append(Record{T: RecDBID, Gen: 1, Extra: "db-1"}))
	mustNoErr(t, w.Append(Record{T: RecPending, Gen: 1, From: 1, To: 2}))
	mustNoErr(t, w.Append(Record{T: RecCommitted, Gen: 1, To: 2, Head: "aa"}))
	recs, err := w.Records()
	if err != nil || len(recs) != 3 || recs[2].T != RecCommitted {
		t.Fatalf("%v %v", recs, err)
	}
}

// 4A: a torn tail needs no repair; appending after it is safe and the fragment carries no meaning.
func TestWitnessTornTailIsSkippedAndAppendContinues(t *testing.T) {
	w := newWitness(t, nil)
	mustNoErr(t, w.Append(Record{T: RecPending, Gen: 1, From: 1, To: 1}))
	b, _ := os.ReadFile(w.path())
	sync := b[len(magic) : len(magic)+syncLen]
	torn := append(append([]byte{}, sync...), 0, 0, 0, 40, '{', '"', 't') // SYNC, len 40, partial payload
	f, _ := os.OpenFile(w.path(), os.O_APPEND|os.O_WRONLY, 0o600)
	f.Write(torn)
	f.Close()
	recs, err := w.Records()
	if err != nil || len(recs) != 1 {
		t.Fatalf("torn tail must be absent: %v %v", recs, err)
	}
	mustNoErr(t, w.Append(Record{T: RecCommitted, Gen: 1, To: 1}))
	recs, err = w.Records()
	if err != nil || len(recs) != 2 {
		t.Fatalf("append after a torn fragment: %v %v", recs, err)
	}
}

func TestWitnessDamageIsDetected(t *testing.T) {
	// replayed record: an old authentic frame appended again
	w := newWitness(t, nil)
	mustNoErr(t, w.Append(Record{T: RecPending, Gen: 1, From: 1, To: 1}))
	one, _ := os.ReadFile(w.path())
	mustNoErr(t, w.Append(Record{T: RecCommitted, Gen: 1, To: 1}))
	hdr := len(magic) + syncLen + macLen
	frame := one[hdr:]
	f, _ := os.OpenFile(w.path(), os.O_APPEND|os.O_WRONLY, 0o600)
	f.Write(frame)
	f.Close()
	if _, err := w.Records(); !errors.Is(err, ErrWitnessDamaged) {
		t.Fatalf("a replayed record must damage the file: %v", err)
	}
	if err := w.Append(Record{T: RecFailed}); !errors.Is(err, ErrWitnessDamaged) {
		t.Fatalf("append on a damaged chain: %v", err)
	}
	// a deleted middle record (gap): the next authentic record does not chain
	w2 := newWitness(t, nil)
	mustNoErr(t, w2.Append(Record{T: RecPending, Gen: 1, From: 1, To: 1}))
	mid, _ := os.ReadFile(w2.path())
	mustNoErr(t, w2.Append(Record{T: RecCommitted, Gen: 1, To: 1}))
	full, _ := os.ReadFile(w2.path())
	mustNoErr(t, os.WriteFile(w2.path(), append(append([]byte{}, full[:hdr]...), full[len(mid):]...), 0o600))
	if _, err := w2.Records(); !errors.Is(err, ErrWitnessDamaged) {
		t.Fatalf("a deleted record must be detected: %v", err)
	}
	// missing file
	w3 := Witness{Dir: newRunDir(t, nil), Keys: testKeyer()}
	if _, err := w3.Records(); !errors.Is(err, ErrWitnessDamaged) {
		t.Fatal("a missing witness is damage (row 7)")
	}
	// a different key cannot read the file
	w4 := newWitness(t, nil)
	w4.Keys = VaultKeyer{Key: func() ([]byte, error) { return []byte("other"), nil }}
	if _, err := w4.Records(); !errors.Is(err, ErrWitnessDamaged) {
		t.Fatal("wrong key must fail closed")
	}
	// no key at all
	w4.Keys = VaultKeyer{Key: func() ([]byte, error) { return nil, errors.New("locked") }}
	if _, err := w4.Records(); !errors.Is(err, ErrWitnessDamaged) {
		t.Fatal("no vault key must fail closed")
	}
}

func hx(s string) []byte { b, _ := hex.DecodeString(s); return b }

func TestClassifyMatrix(t *testing.T) {
	H := []byte{0xaa}
	hh := hex.EncodeToString(H)
	committed := func(n int64) []Record {
		return []Record{{T: RecDBID}, {T: RecCommitted, To: n, Head: hh}}
	}
	pending := func(n, k int64, ending bool, dbhead string) []Record {
		return []Record{{T: RecCommitted, To: n, Head: hh}, {T: RecPending, From: n + 1, To: n + k, DBHead: dbhead, Ending: ending}}
	}
	cases := []struct {
		name string
		in   Freshness
		want Verdict
		row  int
	}{
		{"row 1 consistent", Freshness{Records: committed(5), D: DBHead{5, H}, DBRunning: true}, VerdictProceed, 1},
		{"row 2 crash before commit", Freshness{Records: pending(5, 2, false, hh), D: DBHead{5, H}, DBRunning: true}, VerdictAbandon, 2},
		{"row 2e ending transition", Freshness{Records: pending(5, 2, true, hh), D: DBHead{5, H}, DBRunning: true}, VerdictFailClosed, 2},
		{"row 3 crash after commit", Freshness{Records: pending(5, 2, false, hh), D: DBHead{7, []byte{0xbb}}, DBRunning: true}, VerdictCommit, 3},
		{"row 4 database rolled back", Freshness{Records: committed(5), D: DBHead{3, H}, DBRunning: true}, VerdictFailClosed, 4},
		{"row 4 truncated audit tail", Freshness{Records: committed(9), D: DBHead{8, H}, DBRunning: true}, VerdictFailClosed, 4},
		{"row 5 one ahead, no pending", Freshness{Records: committed(5), D: DBHead{6, H}, DBRunning: true}, VerdictFailClosed, 5},
		{"row 6 same seq, other head", Freshness{Records: committed(5), D: DBHead{5, []byte{0xcc}}, DBRunning: true}, VerdictFailClosed, 6},
		{"row 7 damaged", Freshness{Damaged: true, D: DBHead{5, H}, DBRunning: true}, VerdictFailClosed, 7},
		{"row 7 empty", Freshness{D: DBHead{5, H}, DBRunning: true}, VerdictFailClosed, 7},
		{"row 8 ended vs running", Freshness{Records: append(committed(5), Record{T: RecCommitted, To: 5, Ended: true}), D: DBHead{5, H}, DBRunning: true}, VerdictFailClosed, 8},
		{"row 9 anchor newer", Freshness{Records: committed(5), D: DBHead{5, H}, DBGen: 1, DBRunning: true, AnchorBound: true,
			Anchor: func() (AnchorState, error) { return AnchorState{Found: true, Generation: 2}, nil }}, VerdictFailClosed, 9},
		{"row 9 anchor ended", Freshness{Records: committed(5), D: DBHead{5, H}, DBGen: 1, DBRunning: true, AnchorBound: true,
			Anchor: func() (AnchorState, error) { return AnchorState{Found: true, Generation: 1, Ended: true}, nil }}, VerdictFailClosed, 9},
		{"row 10 anchor unreadable holds", Freshness{Records: committed(5), D: DBHead{5, H}, DBRunning: true, AnchorBound: true,
			Anchor: func() (AnchorState, error) { return AnchorState{}, errors.New("locked keyring") }}, VerdictHold, 10},
		{"row 11 anchor missing", Freshness{Records: committed(5), D: DBHead{5, H}, DBRunning: true, AnchorBound: true,
			Anchor: func() (AnchorState, error) { return AnchorState{}, nil }}, VerdictFailClosed, 11},
		{"row 13 refused generation", Freshness{Records: append(committed(5), Record{T: RecFailed, Row: 4}), D: DBHead{5, H}, DBRunning: true}, VerdictFailClosed, 13},
		{"row 13 pending matching neither", Freshness{Records: pending(5, 2, false, hh), D: DBHead{6, H}, DBRunning: true}, VerdictFailClosed, 13},
		{"row 5 beyond pending", Freshness{Records: pending(5, 2, false, hh), D: DBHead{9, H}, DBRunning: true}, VerdictFailClosed, 5},
		{"row 4 below pending", Freshness{Records: pending(5, 2, false, hh), D: DBHead{4, H}, DBRunning: true}, VerdictFailClosed, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.in)
			if got.Verdict != c.want || got.Row != c.row {
				t.Fatalf("got %+v, want verdict %v row %d", got, c.want, c.row)
			}
		})
	}
	_ = hx
}

// Phase 2 wins over a pending record: no repair is advised on an untrusted state.
func TestClassifyPhase2BeatsPending(t *testing.T) {
	H := []byte{0xaa}
	hh := hex.EncodeToString(H)
	in := Freshness{Records: []Record{{T: RecPending, From: 6, To: 7, DBHead: hh}, {T: RecCommitted, To: 5, Ended: true}},
		D: DBHead{5, H}, DBRunning: true}
	if d := Classify(in); d.Verdict != VerdictFailClosed || d.Row != 8 {
		t.Fatalf("%+v", d)
	}
}

// 13.5 P1 (torn failure record): a crash while appending "failed" must still leave a durable refusal.
func TestFailGenerationLeavesRefusalEvenWhenFailedRecordIsTorn(t *testing.T) {
	fsys := newFaultFS()
	w := newWitness(t, fsys)
	fsys.failAppend = true // the append of the failed record fails (torn/crash)
	if err := w.FailGeneration(7, 4); err == nil {
		t.Fatal("the failed append should surface")
	}
	if !w.Dir.IsRefused(7) {
		t.Fatal("no refusal remained: the next writer could admit work")
	}
	if w.Dir.IsRefused(8) {
		t.Fatal("a refusal is for its own generation only")
	}
	// idempotent
	fsys.failAppend = false
	mustNoErr(t, w.FailGeneration(7, 4))
}

// A refusal file that cannot be written is an error, never silently accepted.
func TestFailGenerationRefusalWriteFailure(t *testing.T) {
	fsys := newFaultFS()
	w := newWitness(t, fsys)
	fsys.failCreate["7.refused"] = true
	if err := w.FailGeneration(7, 4); err == nil {
		t.Fatal("must fail loudly")
	}
}

func TestSecondHostRefused(t *testing.T) {
	dir := newRunDir(t, nil)
	rel, err := AcquireHost(dir)
	mustNoErr(t, err)
	if _, err := AcquireHost(dir); !errors.Is(err, ErrSecondHost) {
		t.Fatalf("a second host must be refused: %v", err)
	}
	rel()
	rel2, err := AcquireHost(dir)
	mustNoErr(t, err)
	rel2()
}

func TestVaultKeyerFailsClosedAndSeparatesDomains(t *testing.T) {
	k := testKeyer()
	a, _ := k.MAC("audit", []byte("ab"), []byte("c"))
	b, _ := k.MAC("audit", []byte("a"), []byte("bc"))
	c, _ := k.MAC("run", []byte("ab"), []byte("c"))
	if string(a) == string(b) || string(a) == string(c) {
		t.Fatal("part boundaries and domains must separate")
	}
	for _, kk := range []VaultKeyer{{}, {Key: func() ([]byte, error) { return nil, errors.New("x") }}, {Key: func() ([]byte, error) { return nil, nil }}} {
		if _, err := kk.MAC("audit"); !errors.Is(err, ErrNoVaultKey) {
			t.Fatalf("%v", err)
		}
	}
}

// ----- against the real store -----

func realStore(t *testing.T) (*store.SQLStore, *sql.DB) {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/dd.db")
	mustNoErr(t, err)
	mustNoErr(t, db.ApplyMigrations())
	t.Cleanup(func() { db.DB.Close() })
	db.DB.SetMaxOpenConns(1)
	return store.New(db.DB, testKeyer()), db.DB
}

type noTokens struct{}

func (noTokens) RevokeTokens(context.Context, uint64) error { return nil }
func (noTokens) CancelQueue(context.Context, uint64) error  { return nil }

func TestStopOrderAgainstRealStore(t *testing.T) {
	s, db := realStore(t)
	gen, err := s.NextGeneration(bg, "p")
	mustNoErr(t, err)
	c := store.Charter{Profile: "p", Generation: gen, Goal: "g",
		Criteria: []store.Criterion{{Kind: "command", Spec: "true", Expected: "ok"}},
		Limits:   store.Limits{TreeDepth: 4, LivePerTree: 30, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: 30, MaxTotal: 100, BudgetMicro: 1_000_000},
		Lines:    []string{store.LineSpending},
		BossPin:  store.Pin{Provider: "a", Model: "m1", Version: "v", PurposePolicy: "p"}, VerifierPin: store.Pin{Provider: "a", Model: "m2", Version: "v", PurposePolicy: "p"},
		PerAsk: store.PerAsk{Declared: true, SpendMicro: 1, Tasks: 1, Access: "edit", Depth: 1, RunSpendMicro: 1, RunTasks: 1}}
	_, err = s.Enter(bg, c, c.Hash(), "operator")
	mustNoErr(t, err)
	mustNoErr(t, s.Register(bg, store.Member{Profile: "p", Generation: gen, ID: "m1", Kind: "pid", Pid: 5, Stamp: "1", Live: true}))
	runs := StoreRuns{Store: s, Profile: "p", By: "operator", Tokens: noTokens{}}
	dir := newRunDir(t, nil)
	mustNoErr(t, dir.CreateSecret(uint64(gen)))
	en := &fakeEnder{}
	sp := &Stopper{Dir: dir, Store: runs, Ender: en, Clock: newStepClock()}
	// a live member blocks quiescent (the store refuses it); the run stays cleanup-pending
	rep := sp.Stop(bg, uint64(gen), "cid", "operator")
	st, _ := runs.State(bg, uint64(gen))
	if rep.Stopped() || st != StateCleanupPending {
		t.Fatalf("state %s\n%s", st, joined(rep))
	}
	// the member is released, a second Stop (recovery) completes it
	mustNoErr(t, s.Release(bg, "p", gen, "m1"))
	rep = sp.Stop(bg, uint64(gen), "cid", "operator")
	if !rep.Stopped() {
		t.Fatalf("%s", joined(rep))
	}
	// the audit tail is comparable with a witness: truncating it is caught (row 4)
	h, err := ReadDBHead(bg, db, "p", gen)
	mustNoErr(t, err)
	if h.Seq < 4 {
		t.Fatalf("head %+v", h)
	}
	w := newWitness(t, nil)
	mustNoErr(t, w.Append(Record{T: RecCommitted, Gen: gen, To: h.Seq, Head: hex.EncodeToString(h.MAC)}))
	recs, _ := w.Records()
	ok := Classify(Freshness{Records: recs, D: h, DBGen: gen})
	if ok.Verdict != VerdictProceed {
		t.Fatalf("%+v", ok)
	}
	// the audit table is insert-only in the store, so a truncated tail is simulated by presenting an earlier
	// head (a restored older database): the witness, which saw the later head, catches it
	early := DBHead{Seq: h.Seq - 1, MAC: h.MAC}
	if d := Classify(Freshness{Records: recs, D: early, DBGen: gen}); d.Verdict != VerdictFailClosed || d.Row != 4 {
		t.Fatalf("a truncated audit tail must be caught by the witness: %+v", d)
	}
}
