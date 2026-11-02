package coderhost

import (
	"fmt"
	"testing"
	"time"
)

// goParsable is what the SQL validity test must equal: the exact layout, a real calendar date and a year of 0001..9999.
func goParsable(s string) bool {
	ts, err := time.Parse(time.RFC3339Nano, s)
	return err == nil && ts.Year() >= 1
}

// Every month x day 28..32 x leap/non-leap year (centuries included), the year bounds, and the clock fields at their
// limits: the SQL side (tsOK) and Go's time.Parse must agree on each string.
func TestTsOKAgreesWithGoParseOnTheCalendar(t *testing.T) {
	st := newStoreT(t)
	var cases []string
	for _, y := range []int{0, 1, 1900, 2000, 2023, 2024, 2026, 2100, 9999} {
		for m := 1; m <= 12; m++ {
			for d := 0; d <= 32; d++ {
				if d != 0 && d < 28 {
					continue
				}
				for _, f := range []string{"", ".5", ".123456789"} {
					cases = append(cases, fmt.Sprintf("%04d-%02d-%02dT12:00:00%sZ", y, m, d, f))
				}
			}
		}
	}
	for _, c := range []string{"2026-10-31T24:00:00Z", "2026-10-31T23:60:00Z", "2026-10-31T23:59:60Z", "2026-10-31T23:59:59Z", "2026-10-31T00:00:00Z", "9999-12-31T23:59:59.999999999Z", "0001-01-01T00:00:00Z"} {
		cases = append(cases, c)
	}
	for _, c := range cases {
		var n int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM (SELECT ? AS c) WHERE `+tsOK("c"), c).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if got, want := n == 1, goParsable(c); got != want {
			t.Errorf("%q: SQL says parsable = %v, time.Parse says %v", c, got, want)
		}
	}
}

// The deadline functions at the ends of the representable range: now == expires_at is expired even when now+1ns is
// not representable, and the year-0001 minimum is live against any later clock.
func TestTsDeadlineAtTheRangeEndpoints(t *testing.T) {
	st := newStoreT(t)
	max := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	min := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		stored        string
		now           time.Time
		expired, live bool
	}{
		{max.Format(time.RFC3339Nano), max, true, false},
		{max.Format(time.RFC3339Nano), max.Add(-time.Nanosecond), false, true},
		{max.Format(time.RFC3339Nano), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), true, false},
		{min.Format(time.RFC3339Nano), min, true, false},
		{min.Format(time.RFC3339Nano), min.Add(time.Nanosecond), true, false},
		{min.Format(time.RFC3339Nano), time.Date(-5, 1, 1, 0, 0, 0, 0, time.UTC), false, true},
		{"2026-10-31T12:00:00Z", time.Date(-5, 1, 1, 0, 0, 0, 0, time.UTC), false, true},
	}
	for _, c := range cases {
		for name, want := range map[string]bool{tsExpired("c", c.now): c.expired, tsLive("c", c.now): c.live} {
			var n int
			if err := st.db.QueryRow(`SELECT COUNT(*) FROM (SELECT ? AS c) WHERE `+name, c.stored).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if (n == 1) != want {
				t.Errorf("stored %s now %v: got %v, want %v for %.50s", c.stored, c.now, n == 1, want, name)
			}
		}
	}
}

// A closed question with a nonzero command id and an unreadable answered_at is never pruned either.
func TestUnparsableClosedQuestionWithCommandIsNeverPruned(t *testing.T) {
	for _, bad := range badStamps {
		st, clk := soloStore(t)
		clk.t = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC).Add(MaxSendAge)
		if _, err := st.db.Exec(`INSERT INTO coder_session_questions (profile_id, conversation_id, class, status, answer, command_id, created_at, answered_at) VALUES ('p', 'solo', 'ambiguity', 'answered', 'subject', 1, ?, ?)`, bad, bad); err != nil {
			t.Fatal(err)
		}
		good := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Format(timeLayout)
		if _, err := st.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
			INSERT INTO coder_session_questions (profile_id, conversation_id, class, status, command_id, created_at, answered_at) SELECT 'p', 'solo', 'ambiguity', 'answered', 1, ?, ? FROM n`, MaxClosedQuestions, good, good); err != nil {
			t.Fatal(err)
		}
		st.pruneClosedQuestions("p", "solo")
		var n int
		_ = st.db.QueryRow(`SELECT COUNT(*) FROM coder_session_questions WHERE answer = 'subject'`).Scan(&n)
		if n != 1 {
			t.Errorf("answered_at %q with command id 1 was pruned", bad)
		}
	}
}
