package coderhost

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Stored values that are not "YYYY-MM-DDTHH:MM:SS[.f]Z". The columns the sites read are NOT NULL, so NULL is covered
// at the helper level (TestTsPolaritiesOnUnparsableValues); here every site gets the other ways to be wrong.
var badStamps = []string{"", "garbage", "2026-10-31 12:00:00Z", "2026-10-31T12:00:00Zjunk", "2026-10-31T12:00:00junk", "2026-10-31T12:00:00+01:00", "2026-02-30T12:00:00Z", "2026-11-31T12:00:00Z", "0000-01-01T00:00:00Z"}

// runBad plants each bad value at a site (cut: a whole-second cutoff) and requires the site's outcome, as the driver
// reports it, to be want. want is the site's documented polarity: see the table in timecmp.go.
func runBad(t *testing.T, want bool, d tsDriver) {
	t.Helper()
	cut := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, bad := range badStamps {
		t.Run(fmt.Sprintf("%q", bad), func(t *testing.T) {
			if got := d(t, cut, bad); got != want {
				t.Fatalf("unparsable %q: outcome = %v, want %v", bad, got, want)
			}
		})
	}
}

// Each test reuses the driver of the matching TestTimestampSite* test; the driver returns "the row was kept / counted /
// not yet due" exactly as there. Group 1 first.

func TestUnparsableHostInsideTurnTokenIsExpired(t *testing.T) {
	if !ancestrySupported {
		t.Skip("process ancestry cannot be read on this OS")
	}
	runBad(t, false, siteHostInsideTurn)
}

func TestUnparsableIssueSessionTokenDropsIt(t *testing.T)     { runBad(t, false, siteIssueDrops) }
func TestUnparsableBudgetRequestCountsForTheCap(t *testing.T) { runBad(t, true, siteBudgetCap) }
func TestUnparsableUnstartedCapIsNeverDropped(t *testing.T)   { runBad(t, true, siteDropCaps) }
func TestUnparsableSpawnIntentPendingLeaseIsLapsed(t *testing.T) {
	runBad(t, false, func(t *testing.T, cut time.Time, s any) bool {
		return !intentClaimed(t, cut.Add(SpawnIntentLease), "pending", "child", 0, s, "")
	})
}
func TestUnparsableSpawnIntentCreateLeaseIsLapsed(t *testing.T) {
	runBad(t, false, func(t *testing.T, cut time.Time, s any) bool {
		return !intentClaimed(t, cut.Add(SpawnCreateLeases*SpawnIntentLease), "pending", "", 0, s, "")
	})
}
func TestUnparsableSpawnIntentSweepingIsLapsed(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(tsFixedLayout)
	runBad(t, false, func(t *testing.T, cut time.Time, s any) bool {
		return !intentClaimed(t, cut.Add(SpawnSweepLease), "sweeping", "child", 1, old, s)
	})
}
func TestUnparsableSpawnRowCountsAndIsNeverDeleted(t *testing.T) { runBad(t, true, siteSpawnRate) }

// The expiry boundary: a token whose expires_at equals now is expired for authentication, so HostInsideTurn must not
// treat it as live either.
func TestHostInsideTurnAndVerifyAgreeAtTheExpiryInstant(t *testing.T) {
	if !ancestrySupported {
		t.Skip("process ancestry cannot be read on this OS")
	}
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, off := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		exp := now.Add(off)
		st := newStoreT(t)
		pinClock(st, now)
		m, _ := master(t, st, "coding")
		running(t, st, m.ConversationID, "t1")
		tok, err := st.IssueSessionToken("p", m.ConversationID, "t1")
		if err != nil {
			t.Fatal(err)
		}
		key, err := st.tokenKey("p")
		if err != nil {
			t.Fatal(err)
		}
		e := exp.Format(timeLayout)
		if _, err := st.db.Exec(`UPDATE coder_session_tokens SET expires_at = ?, pid = ?, mac = ?`, e, os.Getpid(), tokenMAC(key, "p", m.ConversationID, "t1", hashToken(tok), e, os.Getpid(), "")); err != nil {
			t.Fatal(err)
		}
		_, verr := st.VerifySessionToken(tok)
		authLive := verr == nil
		if hostLive := st.HostInsideTurn(); hostLive != authLive || authLive != now.Before(exp) {
			t.Errorf("expires %v after now: HostInsideTurn = %v, VerifySessionToken ok = %v (err %v), want %v", off, hostLive, authLive, verr, now.Before(exp))
		}
	}
}

// Helper polarities, table-driven, NULL included (a nullable column and the NOT NULL columns share the SQL).
func TestTsPolaritiesOnUnparsableValues(t *testing.T) {
	st := newStoreT(t)
	cut := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	values := []any{nil, "", "garbage", "2026-10-31 12:00:00Z", "2026-10-31T12:00:00Zjunk", "2026-10-31T12:00:00junk", "2026-13-31T12:00:00Z", "2026-10-31T12:00:00.1234567890Z", "2026-10-31T12:00:00.Z"}
	for _, v := range values {
		for name, c := range map[string]struct {
			cond string
			want bool
		}{
			"tsExpired":   {tsExpired("c", cut), true},
			"tsLive":      {tsLive("c", cut), false},
			"tsLapsed":    {tsLapsed("c", cut), true},
			"tsInWindow":  {tsInWindow("c", cut), true},
			"tsOlderThan": {tsOlderThan("c", cut), false},
		} {
			var n int
			if err := st.db.QueryRow(`SELECT COUNT(*) FROM (SELECT ? AS c) WHERE `+c.cond, v).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if (n == 1) != c.want {
				t.Errorf("%s(%v) = %v, want %v", name, v, n == 1, c.want)
			}
		}
	}
}

// Parsable values: the equality boundary of each polarity, in both spellings.
func TestTsPolaritiesOnParsableValuesAtTheCutoff(t *testing.T) {
	st := newStoreT(t)
	cut := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, off := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
		for _, v := range spellings(cut.Add(off)) {
			if v == cut.Add(off).Format("2006-01-02T15:04:05.000Z") && off != 0 {
				continue
			}
			at := cut.Add(off)
			for name, c := range map[string]struct {
				cond string
				want bool
			}{
				"tsExpired":   {tsExpired("c", cut), !at.After(cut)},
				"tsLive":      {tsLive("c", cut), at.After(cut)},
				"tsLapsed":    {tsLapsed("c", cut), at.Before(cut)},
				"tsInWindow":  {tsInWindow("c", cut), !at.Before(cut)},
				"tsOlderThan": {tsOlderThan("c", cut), at.Before(cut)},
			} {
				var n int
				if err := st.db.QueryRow(`SELECT COUNT(*) FROM (SELECT ? AS c) WHERE `+c.cond, v).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if (n == 1) != c.want {
					t.Errorf("%s(%s) off %v = %v, want %v", name, v, off, n == 1, c.want)
				}
			}
		}
	}
}

// An extreme clock must not mis-slice the cutoff (four year digits): it clamps to 0001..9999.
func TestTsExtremeCutoffsClamp(t *testing.T) {
	st := newStoreT(t)
	stored := "2026-10-31T12:00:00Z"
	cases := []struct {
		cut        time.Time
		older, win bool // stored is older than the cut / inside the window
	}{
		{time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), true, false},
		{time.Date(12345, 6, 7, 0, 0, 0, 0, time.UTC), true, false},
		{time.Date(-5, 1, 1, 0, 0, 0, 0, time.UTC), false, true},
		{time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), false, true},
	}
	for _, c := range cases {
		for name, want := range map[string]bool{tsOlderThan("c", c.cut): c.older, tsInWindow("c", c.cut): c.win} {
			var n int
			if err := st.db.QueryRow(`SELECT COUNT(*) FROM (SELECT ? AS c) WHERE `+name, stored).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if (n == 1) != want {
				t.Errorf("cut %v: got %v, want %v for %.60s", c.cut, n == 1, want, name)
			}
		}
	}
}
