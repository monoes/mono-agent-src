package coderhost

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/coderspend"
	"github.com/monoes/mono-agent/internal/tasks"
)

// Fourth review of the launcher, P2: a snapshot keeps the recorded float exactly. $0.0000004 used to be written as
// $0.000000, charged as nothing and settled for good.
func TestAMicroDollarChildIsChargedNotRoundedToNothing(t *testing.T) {
	for _, cost := range []float64{4e-7, 0.1234567891, 123456789.123456789} {
		f := newHostFix(t)
		conv := f.endedChild(t, 0, cost)
		f.deleteChild(t, conv, true)
		snaps := f.eventsOfKind(t, "spend")
		if len(snaps) != 1 || snaps[0] != coderspend.Note(conv, cost) {
			t.Fatalf("cost %v: snapshot = %v", cost, snaps)
		}
		got, found, err := f.st.snapshotOf("p", f.se.ID, conv)
		if err != nil || !found || got != cost {
			t.Fatalf("cost %v: snapshot reads back %v, %v, %v", cost, got, found, err)
		}
		f.passes(3)
		if m := f.missionSpent(t); m != cost {
			t.Fatalf("cost %v: meter = %v", cost, m)
		}
	}
}

func TestBothWritersOfAnAmountAgreeAndRoundTrip(t *testing.T) {
	for _, v := range []float64{0, 1.25, 4e-7, 1e-9, 0.1 + 0.2, 1e9, 3.0000004} {
		if coderspend.FormatUSD(v) != formatUSD(v) {
			t.Fatalf("%v: %q != %q", v, coderspend.FormatUSD(v), formatUSD(v))
		}
		if parseUSD(formatUSD(v)) != v {
			t.Fatalf("%v does not read back from %q", v, formatUSD(v))
		}
	}
	if formatUSD(1.25) != "1.250000" || formatUSD(0) != "0.000000" {
		t.Fatalf("the usual format changed: %s %s", formatUSD(1.25), formatUSD(0))
	}
}

// P2: an amount or a total beyond the supported range is refused before anything is committed.
func TestAResolutionBeyondTheSupportedRangeIsRefused(t *testing.T) {
	f := newHostFix(t)
	conv := f.buildWalk(t, walkCases[6])
	f.passes(2)
	if _, err := f.st.ResolveCharge("p", conv, 1e308, false, ""); err == nil {
		t.Fatal("1e308 was accepted")
	}
	if m := f.missionSpent(t); m != 0 || f.chargeRows(t) != 0 {
		t.Fatalf("a refused resolution left meter %v and %d charge rows", m, f.chargeRows(t))
	}
	if _, err := f.st.ResolveCharge("p", conv, 1e9, false, "at the limit"); err != nil {
		t.Fatalf("1e9: %v", err)
	}
}

// P2 at the meter: the total is checked in the transaction, so 1e308 twice never reaches +Inf, even past the verb.
func TestTheMeterRefusesATotalItCannotHold(t *testing.T) {
	f := newHostFix(t)
	st := tasks.NewStore(f.st.db)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET spent_usd = 1e308 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSessionSpend(context.Background(), "p", f.se.ID, 1e308, "x", tasks.Actor{Kind: tasks.Human}); err == nil {
		t.Fatal("1e308 + 1e308 was accepted")
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET spent_usd = 9e8 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddSessionSpend(context.Background(), "p", f.se.ID, 2e8, "x", tasks.Actor{Kind: tasks.Human}); err == nil {
		t.Fatal("a total of 1.1e9 was accepted")
	}
	if m := f.missionSpent(t); m != 9e8 {
		t.Fatalf("meter = %v after refused additions", m)
	}
}

// P2: two skips, or a skip and a positive resolution, leave exactly one charge row and a meter that agrees with it.
func TestRacingResolutionsLeaveOneChargeRow(t *testing.T) {
	for i := 0; i < 12; i++ {
		f := newHostFix(t)
		conv := f.buildWalk(t, walkCases[6])
		f.passes(2)
		var wg sync.WaitGroup
		run := func(skip bool) {
			defer wg.Done()
			if skip {
				_, _ = f.st.ResolveCharge("p", conv, 0, true, "free")
			} else {
				_, _ = f.st.ResolveCharge("p", conv, walkCost, false, "")
			}
		}
		wg.Add(2)
		go run(true)
		go run(i%2 == 0) // a second skip, then a positive resolution
		wg.Wait()
		rows := f.eventsOfKind(t, "charge")
		if len(rows) != 1 {
			t.Fatalf("round %d: %d charge rows: %v", i, len(rows), rows)
		}
		want := 0.0
		if !strings.Contains(rows[0], "skipped") {
			want = walkCost
		}
		if m := f.missionSpent(t); m != want {
			t.Fatalf("round %d: meter %v does not agree with %q", i, m, rows[0])
		}
	}
}

// The writer itself: a charge row that exists makes a zero-cost write a no-op (it is one statement, not a check and an insert).
func TestAZeroCostChargeWriteIsOnceOnly(t *testing.T) {
	f := newHostFix(t)
	conv := f.buildWalk(t, walkCases[6])
	for i := 0; i < 3; i++ {
		if _, _, err := f.st.writeChargeOnce("p", f.se.ID, f.ids[0], conv, 0, conv+" $0.000000 skipped by the operator: x", "operator"); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.chargeRows(t); n != 1 {
		t.Fatalf("%d charge rows", n)
	}
}
