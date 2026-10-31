package coderhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Timestamps are stored as TEXT and compared in SQL. Released databases hold rows written with
// time.RFC3339Nano, which drops a zero fraction ("...:00Z" sorts AFTER "...:00.000000001Z"), next to rows a fixed-width
// writer would produce. Every comparing site must decide the same for both spellings, 1 ns either side of the cutoff.

type tsRow struct {
	name       string
	stamp      string
	atOrAfter  bool // the row is not before the cutoff
	cutoffDesc string
}

// tsCuts: a whole-second cutoff, one 1 ns after it, and a half-second one.
func tsCuts() []time.Time {
	w := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	return []time.Time{w, w.Add(time.Nanosecond), w.Add(500 * time.Millisecond)}
}

func tsRowsFor(cut time.Time) []tsRow {
	var out []tsRow
	for _, d := range []struct {
		name string
		off  time.Duration
		ge   bool
	}{{"1ns-before", -time.Nanosecond, false}, {"equal", 0, true}, {"1ns-after", time.Nanosecond, true}} {
		at := cut.Add(d.off).UTC()
		out = append(out,
			tsRow{name: d.name + "/rfc3339nano", stamp: at.Format(time.RFC3339Nano), atOrAfter: d.ge},
			tsRow{name: d.name + "/fixed", stamp: at.Format(tsFixedLayout), atOrAfter: d.ge})
	}
	return out
}

// tsDriver arranges a store so the site under test cuts at cut, plants one row stamped stamp and says whether the
// site treated the row as at-or-after the cutoff (kept, counted, not yet due).
type tsDriver func(t *testing.T, cut time.Time, stamp any) bool

func runTS(t *testing.T, d tsDriver) { t.Helper(); runTSAs(t, false, d) }

// runTSDeadline is runTS for deadlines: a row stamped exactly at the cutoff is EXPIRED (VerifySessionToken: !now.Before(exp)),
// so only a row strictly after the cutoff counts as live.
func runTSDeadline(t *testing.T, d tsDriver) { t.Helper(); runTSAs(t, true, d) }

func runTSAs(t *testing.T, deadline bool, d tsDriver) {
	t.Helper()
	for _, cut := range tsCuts() {
		for _, r := range tsRowsFor(cut) {
			t.Run(fmt.Sprintf("cut-%s/%s", cut.Format("05.000000000"), r.name), func(t *testing.T) {
				want := r.atOrAfter && !(deadline && strings.HasPrefix(r.name, "equal"))
				if got := d(t, cut, r.stamp); got != want {
					t.Fatalf("row stamped %s against cutoff %s: at-or-after = %v, want %v", r.stamp, cut.Format(tsFixedLayout), got, want)
				}
			})
		}
	}
}

func pinClock(st *Store, at time.Time) { st.now = func() time.Time { return at } }

// --- GROUP 1 ---------------------------------------------------------------------------------------------------

func TestTimestampSiteHostInsideTurnTokenExpiry(t *testing.T) {
	if !ancestrySupported {
		t.Skip("process ancestry cannot be read on this OS")
	}
	runTSDeadline(t, siteHostInsideTurn)
}

func siteHostInsideTurn(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st := newStoreT(t)
	pinClock(st, cut)
	if _, err := st.db.Exec(`INSERT INTO coder_session_tokens (token_hash, profile_id, conversation_id, turn_id, created_at, expires_at, pid, stamp, mac) VALUES ('h', 'p', 'c', 't', ?, ?, ?, '', '')`,
		stamp, stamp, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	return st.HostInsideTurn()
}

func TestTimestampSiteIssueSessionTokenDropsExpiredRows(t *testing.T) {
	runTSDeadline(t, siteIssueDrops)
}

func siteIssueDrops(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st := newStoreT(t)
	pinClock(st, cut)
	m, _ := master(t, st, "coding")
	running(t, st, m.ConversationID, "t1")
	if _, err := st.db.Exec(`INSERT INTO coder_session_tokens (token_hash, profile_id, conversation_id, turn_id, created_at, expires_at, pid, stamp, mac) VALUES ('other', 'p', 'zz', 't', ?, ?, 0, '', '')`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := st.IssueSessionToken("p", m.ConversationID, "t1"); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_tokens WHERE token_hash = 'other'`).Scan(&n)
	return n == 1
}

func TestTimestampSiteBudgetRequestHourlyCap(t *testing.T) {
	runTS(t, siteBudgetCap)
}

func siteBudgetCap(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	f, _ := budgetFix(t)
	pinClock(f.st, cut.Add(time.Hour))
	fill := cut.Add(time.Hour - time.Minute).Format(tsFixedLayout)
	for i := 0; i < MaxBudgetRequestsPerHour-1; i++ {
		f.fillBudgetRow(t, fill)
	}
	f.fillBudgetRow(t, stamp)
	_, err := f.st.RequestBudget(f.scope(), "p", f.se.ID, TargetChild, f.ids[0], 2, "need more")
	if err != nil && !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	return errors.Is(err, ErrLimit) // refused: the planted row counted
}

func (f *mcFix) fillBudgetRow(t *testing.T, createdAt any) {
	t.Helper()
	if _, err := f.st.db.Exec(`INSERT INTO coder_budget_requests (profile_id, session_id, lead_conversation_id, target, target_conversation_id, amount_usd, budget_usd, spent_usd, status, created_at)
		VALUES ('p', 424242, ?, 'lead', ?, 1, 1, 0, 'declined', ?)`, f.lead.ConversationID, f.lead.ConversationID, createdAt); err != nil {
		t.Fatal(err)
	}
}

func TestTimestampSiteDropUnstartedCaps(t *testing.T) {
	runTS(t, siteDropCaps)
}

func siteDropCaps(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st := newStoreT(t)
	pinClock(st, cut.Add(time.Minute))
	if _, err := st.db.Exec(`INSERT INTO coder_session_turn_caps (profile_id, conversation_id, turn_id, cap_usd, started, created_at) VALUES ('p', 'c', 't', 1, 0, ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	st.DropUnstartedCaps()
	var n int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_turn_caps`).Scan(&n)
	return n == 1
}

// intentClaimed plants one spawn intent and says whether a sweep at now took it (its generation moved or it is gone).
func intentClaimed(t *testing.T, now time.Time, state, child string, attempts int, created, claimed any) bool {
	t.Helper()
	st := newStoreT(t)
	pinClock(st, now)
	if _, err := st.db.Exec(`INSERT INTO coder_spawn_intents (profile_id, spawner, scope_hash, child, state, generation, created_at, claimed_at, attempts) VALUES ('p', 's', 'h', ?, ?, 1, ?, ?, ?)`,
		child, state, created, claimed, attempts); err != nil {
		t.Fatal(err)
	}
	_, _ = st.SweepSpawnIntents(context.Background(), 20)
	var gen int
	if err := st.db.QueryRow(`SELECT generation FROM coder_spawn_intents WHERE profile_id = 'p' AND spawner = 's'`).Scan(&gen); err != nil {
		return true // the sweep ended it
	}
	return gen != 1
}

func TestTimestampSiteSpawnIntentPendingLease(t *testing.T) {
	runTS(t, func(t *testing.T, cut time.Time, stamp any) bool {
		return !intentClaimed(t, cut.Add(SpawnIntentLease), "pending", "child", 0, stamp, "")
	})
}

func TestTimestampSiteSpawnIntentCreateLease(t *testing.T) {
	runTS(t, func(t *testing.T, cut time.Time, stamp any) bool {
		return !intentClaimed(t, cut.Add(SpawnCreateLeases*SpawnIntentLease), "pending", "", 0, stamp, "")
	})
}

func TestTimestampSiteSpawnIntentSweepingBackOff(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(tsFixedLayout)
	for attempts := 1; attempts <= SpawnSweepAttempts+1; attempts++ {
		t.Run(fmt.Sprintf("attempts-%d", attempts), func(t *testing.T) {
			runTS(t, func(t *testing.T, cut time.Time, stamp any) bool {
				lease := SpawnSweepLease << min(attempts-1, SpawnSweepAttempts)
				return !intentClaimed(t, cut.Add(lease), "sweeping", "child", attempts, old, stamp)
			})
		})
	}
}

func TestTimestampSiteSpawnRateWindow(t *testing.T) {
	runTS(t, siteSpawnRate)
}

func siteSpawnRate(t *testing.T, cut time.Time, stamp any) bool {
	t.Helper()
	st := newStoreT(t)
	pinClock(st, cut.Add(SpawnRateWindow))
	fill := cut.Add(SpawnRateWindow - time.Minute).Format(tsFixedLayout)
	for i := 0; i < SpawnRateMax-1; i++ {
		if _, err := st.db.Exec(`INSERT INTO coder_session_spawns (profile_id, parent, child, created_at) VALUES ('p', 'm', '', ?)`, fill); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.Exec(`INSERT INTO coder_session_spawns (profile_id, parent, child, created_at) VALUES ('p', 'm', 'planted', ?)`, stamp); err != nil {
		t.Fatal(err)
	}
	err := st.takeSpawnSlot("p", "m")
	if err != nil && !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	var kept int
	_ = st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_spawns WHERE child = 'planted'`).Scan(&kept)
	counted := errors.Is(err, ErrLimit)
	if (kept == 1) != counted {
		t.Fatalf("the row was kept = %v but counted = %v: the cleanup and the count disagree", kept == 1, counted)
	}
	return counted
}
