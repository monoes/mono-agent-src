package task

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

func TestTaskHeldNoteSaysWhoHoldsATaskAndWhetherTheClaimHasRunOut(t *testing.T) {
	zone := localNoonZone(t)
	soon, tomorrow := time.Now().Add(time.Hour), time.Now().Add(30*time.Hour)
	for _, c := range []struct {
		name  string
		claim *tasks.Claim
		want  string
	}{
		{"no claim", nil, ""},
		{"a live claim that ends today", &tasks.Claim{By: "bot", Until: soon}, "bot until " + soon.In(zone).Format("15:04")},
		{"a live claim that ends another day", &tasks.Claim{By: "bot", Until: tomorrow}, "bot until " + tomorrow.In(zone).Format("01-02 15:04")},
		// The printer shows the store's verdict, whatever the time says.
		{"a stale claim", &tasks.Claim{By: "bot", Until: soon, Stale: true}, "stale: bot"},
		{"a stale claim of long ago", &tasks.Claim{By: "bot", Until: time.Now().Add(-72 * time.Hour), Stale: true}, "stale: bot"},
	} {
		if got := heldNote(tasks.Task{Claim: c.claim}); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// A lease is up to 24 hours, so a time of day alone can mean today or tomorrow: the date
// is written when the lease does not end today (in local time, which is not UTC here).
func TestTaskLeaseEndShowsTheDateOnlyWhenItIsNotToday(t *testing.T) {
	zone := time.FixedZone("Test", 5*3600+30*60)
	setLocalZone(t, zone)
	at := func(month, day, hour, minute int) time.Time {
		return time.Date(2026, time.Month(month), day, hour, minute, 0, 0, zone)
	}
	for _, c := range []struct {
		name       string
		now, until time.Time
		want       string
	}{
		{"later today", at(10, 6, 12, 0), at(10, 6, 13, 0), "13:00"},
		{"a minute before midnight", at(10, 6, 12, 0), at(10, 6, 23, 59), "23:59"},
		{"just after midnight", at(10, 6, 12, 0), at(10, 7, 0, 0), "10-07 00:00"},
		{"tomorrow morning", at(10, 6, 12, 0), at(10, 7, 9, 15), "10-07 09:15"},
		{"over the end of a month", at(10, 31, 23, 0), at(11, 1, 0, 30), "11-01 00:30"},
		{"over the end of a year", at(12, 31, 23, 0), time.Date(2027, 1, 1, 0, 30, 0, 0, zone), "01-01 00:30"},
		{"the same day in a month of its own", at(10, 6, 12, 0), at(11, 6, 12, 0), "11-06 12:00"},
		// The UTC date and the local date disagree here: 22:00 and 01:00 local are the same UTC
		// day, 04:00 and 07:00 local are two.
		{"one UTC day, two local days", at(10, 6, 22, 0), at(10, 7, 1, 0), "10-07 01:00"},
		{"two UTC days, one local day", at(10, 6, 4, 0), at(10, 6, 7, 0), "07:00"},
		// The times are read in the local zone whatever zone they come in.
		{"given in UTC, the same day", at(10, 6, 12, 0).UTC(), at(10, 6, 13, 0).UTC(), "13:00"},
		{"given in UTC, another day", at(10, 6, 12, 0).UTC(), at(10, 7, 9, 15).UTC(), "10-07 09:15"},
	} {
		if got := leaseEnd(c.until, c.now); got != c.want {
			t.Errorf("%s: a lease to %v from %v is written %q, want %q", c.name, c.until, c.now, got, c.want)
		}
	}
}

func TestTaskALeaseThatEndsAnotherDayCarriesItsDateInEveryView(t *testing.T) {
	db := newTaskTestDB(t)
	zone := localNoonZone(t)
	now := time.Now()
	ids := seedTaskRows(t, db,
		taskSeed{title: "today", status: "in_progress", holder: "bot", until: now.Add(time.Hour)},
		taskSeed{title: "tomorrow", status: "in_progress", holder: "late", until: now.Add(30 * time.Hour)},
	)
	today := "bot until " + now.Add(time.Hour).In(zone).Format("15:04")
	tomorrow := "late until " + now.Add(30*time.Hour).In(zone).Format("01-02 15:04")
	table, _, err1 := runTask(t, db, "default", false, "", "list")
	board, _, err2 := runTask(t, db, "default", false, "", "board")
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	for _, want := range []string{today, tomorrow} {
		if !strings.Contains(table, want+"\n") || !strings.Contains(board, "["+want+"]\n") {
			t.Errorf("%q is missing from the list or the board:\n%s\n%s", want, table, board)
		}
	}
	for i, want := range []string{"Held by:  " + today, "Held by:  " + tomorrow} {
		shown, _, err := runTask(t, db, "default", false, "", "show", strconv.FormatInt(ids[i], 10))
		if err != nil || !strings.Contains(shown, want+"\n") {
			t.Errorf("show #%d: %v, want the line %q in:\n%s", ids[i], err, want, shown)
		}
	}
}
