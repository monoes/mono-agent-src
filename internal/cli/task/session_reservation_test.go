package task

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

type reservationsDoc struct {
	Session      int64 `json:"session"`
	Reservations []struct {
		ID         int64   `json:"id"`
		Session    int64   `json:"session"`
		Worker     string  `json:"worker"`
		State      string  `json:"state"`
		AmountUSD  float64 `json:"amount_usd"`
		ChargedUSD float64 `json:"charged_usd"`
		HeldUSD    float64 `json:"held_usd"`
		EndNote    string  `json:"end_note"`
	} `json:"reservations"`
}

// reservedSession makes a running session with a $10 budget and a reservation of $4 on it, taken a
// while ago (lapsed: true) or just now. It returns the session and the reservation ids.
func reservedSession(t *testing.T, db string, lapsed bool) (session, resv int64) {
	t.Helper()
	session = runningSession(t, db)
	var d sessionDoc
	mustTaskJSON(t, db, "default", &d, "", "session", "budget", id(session), "--usd", "10")
	d2, err := storage.NewDatabase(db)
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	s := tasks.NewStore(d2.DB)
	if lapsed {
		past := time.Now().Add(-time.Hour)
		s.SetClock(func() time.Time { return past })
	}
	r, err := s.Reserve(context.Background(), "default", session, "owner-1", "worker-1", 4)
	if err != nil {
		t.Fatal(err)
	}
	return session, r.ID
}

func reservationList(t *testing.T, db string, session int64) reservationsDoc {
	t.Helper()
	var got reservationsDoc
	mustTaskJSON(t, db, "default", &got, "", "session", "reservation", "list", id(session))
	return got
}

func TestSessionReservationListShape(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	session, rid := reservedSession(t, db, false)
	got := reservationList(t, db, session)
	if got.Session != session || len(got.Reservations) != 1 {
		t.Fatalf("list = %+v", got)
	}
	r := got.Reservations[0]
	if r.ID != rid || r.State != "live" || r.AmountUSD != 4 || r.ChargedUSD != 0 || r.HeldUSD != 4 || r.Worker != "worker-1" {
		t.Fatalf("row = %+v", r)
	}
	out, _, _ := runTask(t, db, "default", true, "", "session", "reservation", "list", id(session))
	if strings.Contains(out, "owner-1") {
		t.Fatalf("the owner handle is printed: %s", out)
	}
	text, _, err := runTask(t, db, "default", false, "", "session", "reservation", "list", id(session))
	if err != nil || !strings.Contains(text, "#"+id(rid)) {
		t.Fatalf("text list = %q, %v", text, err)
	}
	empty := runningSession(t, db)
	if g := reservationList(t, db, empty); len(g.Reservations) != 0 {
		t.Fatalf("an empty session lists %+v", g)
	}
}

func TestSessionReservationListErrors(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	session, _ := reservedSession(t, db, false)
	failedTaskJSON(t, db, "default", 2, "session", "reservation", "list", "9999")
	failedTaskJSON(t, db, "default", 3, "session", "reservation", "list", "x")
	failedTaskJSON(t, db, "default", 3, "session", "reservation", "list")
	// Another profile does not see it.
	out, _, err := runTask(t, db, "no-such", true, "", "session", "reservation", "list", id(session))
	if err == nil {
		t.Fatalf("another profile read it: %s", out)
	}
	// An unbound agent may read (like session show), and cannot change anything.
	var got reservationsDoc
	mustTaskJSON(t, db, "default", &got, "", "session", "reservation", "list", id(session), "--as", "bot")
	if len(got.Reservations) != 1 {
		t.Fatalf("agent list = %+v", got)
	}
}

func TestSessionReservationResolveByTheOperator(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	session, rid := reservedSession(t, db, true) // lapsed past the grace: the resolve sweeps it first
	var res struct {
		Reservation int64   `json:"reservation"`
		State       string  `json:"state"`
		ChargedUSD  float64 `json:"charged_usd"`
	}
	mustTaskJSON(t, db, "default", &res, "", "session", "reservation", "resolve", id(rid), "--usd", "1.5", "--note", "from the bill")
	if res.Reservation != rid || res.State != "resolved" || res.ChargedUSD != 1.5 {
		t.Fatalf("resolve = %+v", res)
	}
	r := reservationList(t, db, session).Reservations[0]
	if r.State != "resolved" || r.ChargedUSD != 1.5 || r.HeldUSD != 0 || r.EndNote != "from the bill" {
		t.Fatalf("row = %+v", r)
	}
	if st := sessionState(t, db, session); st.Session.SpentUSD != 1.5 {
		t.Fatalf("spent = %v, want 1.5", st.Session.SpentUSD)
	}
	// A second resolve is refused and charges nothing more.
	failedTaskJSON(t, db, "default", 3, "session", "reservation", "resolve", id(rid), "--usd", "2")
	if st := sessionState(t, db, session); st.Session.SpentUSD != 1.5 {
		t.Fatalf("a second resolve charged: %v", st.Session.SpentUSD)
	}
}

func TestSessionReservationResolveRefusals(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	session, live := reservedSession(t, db, false)
	_, lapsed := reservedSession(t, db, true)
	rs := strconv.FormatInt
	cases := []struct {
		name string
		exit int
		args []string
	}{
		{"unknown id", 2, []string{"9999", "--usd", "1"}},
		{"live in grace", 3, []string{rs(live, 10), "--usd", "1"}},
		{"no usd", 3, []string{rs(lapsed, 10)}},
		{"negative", 3, []string{rs(lapsed, 10), "--usd", "-1"}},
		{"NaN", 3, []string{rs(lapsed, 10), "--usd", "NaN"}},
		{"Inf", 3, []string{rs(lapsed, 10), "--usd", "Inf"}},
		{"huge", 3, []string{rs(lapsed, 10), "--usd", "1e12"}},
		{"bad id", 3, []string{"abc", "--usd", "1"}},
	}
	for _, c := range cases {
		failedTaskJSON(t, db, "default", c.exit, append([]string{"session", "reservation", "resolve"}, c.args...)...)
	}
	// Nothing above changed a row or the spend.
	if st := sessionState(t, db, session); st.Session.SpentUSD != 0 {
		t.Fatalf("spent = %v", st.Session.SpentUSD)
	}
	if r := reservationList(t, db, session).Reservations[0]; r.State != "live" || r.ChargedUSD != 0 {
		t.Fatalf("row = %+v", r)
	}
	// Another profile cannot resolve it.
	if _, _, err := runTask(t, db, "no-such", true, "", "session", "reservation", "resolve", rs(lapsed, 10), "--usd", "1"); err == nil {
		t.Fatal("another profile resolved it")
	}
}

func TestSessionReservationResolveIsTheOperatorsAlone(t *testing.T) {
	t.Parallel()
	db := newTaskTestDB(t)
	session, rid := reservedSession(t, db, true)
	for name, ctx := range opsAgentContexts {
		t.Run(name, func(t *testing.T) {
			args := append([]string{"session", "reservation", "resolve", id(rid), "--usd", "1"}, ctx(t)...)
			out, _, err := runTask(t, db, "default", true, "", args...)
			if exitCode(err) != 3 {
				t.Fatalf("exit %d (%v)\n%s", exitCode(err), err, out)
			}
			var doc struct {
				Code string `json:"code"`
			}
			if json.Unmarshal([]byte(out), &doc); doc.Code != "operator_only" {
				t.Fatalf("code %q\n%s", doc.Code, out)
			}
		})
	}
	if r := reservationList(t, db, session).Reservations[0]; r.State == "resolved" || r.ChargedUSD != 0 {
		t.Fatalf("an agent changed the row: %+v", r)
	}
}
