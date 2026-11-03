package doomsday

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

func rec(t string, to int64, head string) stop.Record {
	return stop.Record{T: t, Gen: 1, To: to, Head: head}
}

// The comparison of a head with the witness: every row of the table, so a cut tail can never read as anchored.
func TestCheckHeadAgainstWitness(t *testing.T) {
	mac := []byte{1, 2, 3}
	hx := hex.EncodeToString(mac)
	cases := []struct {
		name     string
		recs     []stop.Record
		seq      int64
		mac      []byte
		anchored bool
		reason   string
	}{
		{"head equals the last committed record", []stop.Record{rec(stop.RecCommitted, 1, "aa"), rec(stop.RecCommitted, 3, hx)}, 3, mac, true, ""},
		{"tail cut below the witness", []stop.Record{rec(stop.RecCommitted, 1, "aa"), rec(stop.RecCommitted, 3, hx)}, 2, mac, false, store.ReasonBehindWitness},
		{"tail cut to nothing but the first row", []stop.Record{rec(stop.RecCommitted, 5, hx)}, 1, mac, false, store.ReasonBehindWitness},
		{"same seq, other MAC", []stop.Record{rec(stop.RecCommitted, 3, "ff")}, 3, mac, false, store.ReasonWitnessMismatch},
		{"rows past the witness with nothing announcing them", []stop.Record{rec(stop.RecCommitted, 3, hx)}, 4, mac, false, store.ReasonWitnessMismatch},
		{"one announced transition in flight", []stop.Record{rec(stop.RecCommitted, 3, "aa"), rec(stop.RecPending, 4, "")}, 4, mac, true, ""},
		{"a pending record from before the committed one does not announce", []stop.Record{rec(stop.RecPending, 4, ""), rec(stop.RecCommitted, 3, "aa")}, 4, mac, false, store.ReasonWitnessMismatch},
		{"no record of the generation", nil, 3, mac, false, AnchorNoRecord},
		{"the witness knows only a newer generation", []stop.Record{{T: stop.RecCommitted, Gen: 9, To: 3, Head: hx}}, 3, mac, false, store.ReasonWitnessAhead},
		{"a failed freshness check of the generation", []stop.Record{rec(stop.RecCommitted, 3, hx), {T: stop.RecFailed, Gen: 1, Row: 5}}, 3, mac, false, store.ReasonWitnessMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, why := CheckHeadAgainstWitness(c.recs, 1, c.seq, c.mac)
			if ok != c.anchored || why != c.reason {
				t.Fatalf("got %v %q, want %v %q", ok, why, c.anchored, c.reason)
			}
		})
	}
}

func overviewOf(t *testing.T, w *wireRig) (Overview, string) {
	t.Helper()
	w.deps.JSONOutput = func() bool { return true }
	out, err := run(t, w.deps, "", "overview")
	if err != nil {
		t.Fatal(err, out)
	}
	var o Overview
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatal(err, out)
	}
	return o, out
}

// End to end on the real store and the real witness file of a started run: the badge is yes while the head matches
// the witness, and a cut tail (consistent in itself) is never yes, whatever the store says.
func TestOverviewBadgeFollowsTheWitnessOnARealRun(t *testing.T) {
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.start(t)
	e := w.w.Engine()
	for i := 0; i < 3; i++ {
		if _, err := e.Store.AppendAudit(context.Background(), "p1", 1, "x.note", "row"); err != nil {
			t.Fatal(err)
		}
	}
	o, out := overviewOf(t, w)
	if o.Integrity.ChainVerified != VerifiedYes || o.Integrity.HeadSeq < 4 || o.Spend == nil || o.Spend.CountersAuthenticated {
		t.Fatalf("a healthy run: %s", out)
	}

	// The same chain, read by an install that wired no witness: consistent, not anchored, not yes.
	noWitness := w.w.Engine()
	noWitness.HeadAnchor = nil
	w.deps.Engine = func() Engine { return noWitness }
	if o, out = overviewOf(t, w); o.Integrity.ChainVerified != VerifiedUnanchored || o.Integrity.Reason != AnchorNoWitness || o.Tree == nil {
		t.Fatalf("no witness: %s", out)
	}
	w.deps.Engine = func() Engine { return e }

	// Cut the tail down to the run.enter row and its successor: internally consistent, but the witness remembers more.
	if _, err := db.DB.Exec(`DROP TRIGGER doomsday_audit_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`DELETE FROM doomsday_audit WHERE profile = 'p1' AND seq > 2`); err != nil {
		t.Fatal(err)
	}
	o, out = overviewOf(t, w)
	if o.Integrity.ChainVerified != VerifiedNo || o.Integrity.Reason != store.ReasonBehindWitness || o.Tree != nil || o.Spend != nil {
		t.Fatalf("a cut tail was not caught: %s", out)
	}
	feed, err := run(t, w.deps, "", "feed")
	if err != nil || strings.Contains(feed, `"seq"`) || !strings.Contains(feed, `"chain_verified":"no"`) {
		t.Fatalf("a cut tail still shows rows: %v %s", err, feed)
	}

	// Every row cut: an empty chain is never yes.
	if _, err := db.DB.Exec(`DELETE FROM doomsday_audit WHERE profile = 'p1'`); err != nil {
		t.Fatal(err)
	}
	if o, out = overviewOf(t, w); o.Integrity.ChainVerified == VerifiedYes {
		t.Fatalf("an empty chain is yes: %s", out)
	}
}

// store (real migrated database, real rows) -> view -> CLI JSON, against a golden: the hand-made views of the other
// goldens cannot catch a field the store fills and the contract drops. The key is fixed, so the document is stable.
func TestControlRoomGoldenFromTheRealStore(t *testing.T) {
	realNow := now
	now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	t.Cleanup(func() { now = realNow })
	e := realEngine(t)
	ctx := context.Background()
	ch := store.Charter{Profile: "p1", Generation: 1, Goal: "g", Criteria: []store.Criterion{{Kind: "command", Spec: "true", Expected: "ok"}},
		Limits:  store.Limits{TreeDepth: 4, LivePerTree: 5, SpawnRate: 1, OpenSessions: 5, MaxConcurrent: 5, MaxTotal: 10, BudgetMicro: 9_000_000},
		PerAsk:  store.PerAsk{Declared: true, SpendMicro: 1000, Tasks: 2, Access: "edit", Depth: 2, RunSpendMicro: 50_000, RunTasks: 9, AnswerClasses: []string{"context"}},
		BossPin: store.Pin{Provider: "a", Model: "m", Version: "1", PurposePolicy: "j"}, VerifierPin: store.Pin{Provider: "a", Model: "n", Version: "1", PurposePolicy: "j"}}
	if _, err := e.Store.Enter(ctx, ch, ch.Hash(), "operator"); err != nil {
		t.Fatal(err)
	}
	sq := e.Store.(*store.SQLStore)
	for i, k := range []string{"session", "session", "tool"} {
		if err := sq.Register(ctx, store.Member{Profile: "p1", Generation: 1, ID: fmt.Sprintf("m%d", i), Kind: k, Pid: 10 + i, Stamp: "s"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sq.ConsumeCumulative(ctx, "p1", 1, "ask_spend_micro", "ap-1", 700, 50_000); err != nil {
		t.Fatal(err)
	}
	if _, err := sq.AppendAudit(ctx, "p1", 1, "boss.note", "free text that must not reach the output"); err != nil {
		t.Fatal(err)
	}
	r := newRig()
	r.deps.Engine = func() Engine { return e }
	r.deps.JSONOutput = func() bool { return true }
	for name, args := range map[string][]string{"overview": {"overview"}, "feed": {"feed", "--limit", "10"}} {
		out, err := run(t, r.deps, "", args...)
		if err != nil {
			t.Fatal(err, out)
		}
		path := filepath.Join("testdata", "controlroom_realstore_"+name+".golden.json")
		if *updateGolden {
			if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil || out != string(want) {
			t.Fatalf("%s: store-to-JSON changed (%v):\n got %s\nwant %s", name, err, out, want)
		}
	}
}
