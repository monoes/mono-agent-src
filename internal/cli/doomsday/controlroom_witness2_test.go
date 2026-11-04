package doomsday

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The reason the control room reports when the witness knows a newer generation than the database holds. A literal,
// so these tests state the contract rather than follow a constant.
const reasonAhead = "witness_ahead_generation"

func TestCheckHeadAgainstWitnessKnowsANewerGeneration(t *testing.T) {
	mac := []byte{1, 2, 3}
	hx := hex.EncodeToString(mac)
	g1 := stop.Record{T: stop.RecCommitted, Gen: 1, To: 5, Head: hx}
	cases := []struct {
		name     string
		recs     []stop.Record
		anchored bool
		reason   string
	}{
		{"a newer generation committed", []stop.Record{g1, {T: stop.RecCommitted, Gen: 2, To: 1, Head: "aa"}}, false, reasonAhead},
		{"a newer generation failed closed", []stop.Record{g1, {T: stop.RecFailed, Gen: 2, Row: 5}}, false, reasonAhead},
		{"a newer generation ended", []stop.Record{g1, {T: stop.RecAbandoned, Gen: 2, Ended: true}}, false, reasonAhead},
		{"a newer generation only announced (a refused entry leaves that)", []stop.Record{g1, {T: stop.RecPending, Gen: 2, To: 1}}, true, ""},
		{"a newer generation with only a header-like record", []stop.Record{g1, {T: stop.RecHeader, Gen: 2}}, true, ""},
		{"an announced transition naming another head", []stop.Record{{T: stop.RecCommitted, Gen: 1, To: 4, Head: "aa"}, {T: stop.RecPending, Gen: 1, To: 5, Head: "ff"}}, false, store.ReasonWitnessMismatch},
		{"an announced transition of ANOTHER generation does not announce this head", []stop.Record{{T: stop.RecCommitted, Gen: 1, To: 4, Head: "aa"}, {T: stop.RecPending, Gen: 2, To: 5, Head: hx}}, false, store.ReasonWitnessMismatch},
		{"an announced transition of another generation with no head either", []stop.Record{{T: stop.RecCommitted, Gen: 1, To: 4, Head: "aa"}, {T: stop.RecPending, Gen: 2, To: 5}}, false, store.ReasonWitnessMismatch},
		// The documented crash window is one transition: committed 3 and pending 9 with the database cut to 4 is not it.
		{"a pending record naming a later seq than the head", []stop.Record{{T: stop.RecCommitted, Gen: 1, To: 3, Head: "aa"}, {T: stop.RecPending, Gen: 1, To: 9, Head: hx}}, false, store.ReasonWitnessMismatch},
		{"a pending record naming a later seq than the head, no head named", []stop.Record{{T: stop.RecCommitted, Gen: 1, To: 3, Head: "aa"}, {T: stop.RecPending, Gen: 1, To: 9}}, false, store.ReasonWitnessMismatch},
		{"an announced transition naming this head", []stop.Record{{T: stop.RecCommitted, Gen: 1, To: 4, Head: "aa"}, {T: stop.RecPending, Gen: 1, To: 5, Head: hx}}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, why := CheckHeadAgainstWitness(c.recs, 1, 5, mac)
			if ok != c.anchored || why != c.reason {
				t.Fatalf("got %v %q, want %v %q", ok, why, c.anchored, c.reason)
			}
		})
	}
}

// The newest generation's rows removed from a real store while the real witness remembers it: never yes.
func TestOverviewNewestGenerationRemovedIsNotYes(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	e := w.w.Engine()
	wit := stop.Witness{Dir: w.w.RunDir, Keys: w.w.Witness.Keys}
	if err := wit.Append(stop.Record{T: stop.RecCommitted, Gen: 2, To: 1, Head: "aa"}); err != nil {
		t.Fatal(err)
	}
	w.deps.Engine = func() Engine { return e }
	o, out := overviewOf(t, w)
	if o.Integrity.ChainVerified != VerifiedNo || o.Integrity.Reason != reasonAhead || o.Tree != nil {
		t.Fatalf("a witness ahead of the database: %s", out)
	}
}

type noRunStore struct{ store.Store }

func (noRunStore) ReadView(context.Context, string, int64, store.ViewQuery) (store.View, error) {
	return store.View{}, store.ErrNoRun
}

// Every row of the profile gone (no run row at all) while the witness remembers a generation: not "no run".
func TestOverviewNoRunRowsButTheWitnessRemembersIsNotNoRun(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	e := *w.w.Engine()
	e.Store = noRunStore{e.Store}
	w.deps.Engine = func() Engine { return &e }
	o, out := overviewOf(t, w)
	if !o.Available || o.Integrity.ChainVerified != VerifiedNo || o.Integrity.Reason != reasonAhead {
		t.Fatalf("removed rows read as no run: %s", out)
	}
}

// A transition committed between the snapshot and the witness read is not a cut tail: the second read agrees.
func TestOverviewTransitionBetweenSnapshotAndWitnessIsNotBehind(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	e := w.w.Engine()
	orig, injected := e.HeadAnchor, 0
	e.HeadAnchor = func(g, s int64, m []byte) (bool, string) {
		if injected == 0 {
			injected++
			if _, err := e.Store.AppendAudit(context.Background(), "p1", 1, "x.note", "late"); err != nil {
				t.Error(err)
			}
		}
		return orig(g, s, m)
	}
	w.deps.Engine = func() Engine { return e }
	o, out := overviewOf(t, w)
	if injected != 1 || o.Integrity.ChainVerified != VerifiedYes {
		t.Fatalf("a healthy run read %q (%s) after one transition between the reads: %s", o.Integrity.ChainVerified, o.Integrity.Reason, out)
	}
}

// A witness made under another profile's key is no witness for this one: it can never make the badge yes.
func TestOverviewWitnessOfAnotherProfileIsNotYes(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	other := w.w
	ws := *other.Witness
	ws.Keys = store.NewHMACKeyer([]byte("another profile's key"))
	other.Witness = &ws
	e := w.w.Engine()
	e.HeadAnchor = other.headAnchor()
	w.deps.Engine = func() Engine { return e }
	if o, out := overviewOf(t, w); o.Integrity.ChainVerified == VerifiedYes {
		t.Fatalf("a foreign witness anchored the chain: %s", out)
	}
}
