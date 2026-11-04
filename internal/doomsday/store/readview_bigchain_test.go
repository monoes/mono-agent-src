package store

import (
	"testing"
)

// The walk verifies the whole chain, not the returned page: tampering far outside the page must withhold everything.
// A chain of 5000 rows (25 pages of 200); each tamper is made, checked on three pages, and put back.
func TestReadViewVerifiesTheWholeChainNotThePage(t *testing.T) {
	s, db := newStore(t)
	r := enter(t, s, "p")
	appendN(t, s, r, 4999) // run.enter is row 1
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`DROP TRIGGER doomsday_audit_no_update`)
	exec(`DROP TRIGGER doomsday_audit_no_delete`)

	check := func(name string, wantVerified bool) {
		t.Helper()
		for _, q := range []ViewQuery{{FeedLimit: 200}, {FeedBefore: 100, FeedLimit: 200}, {FeedBefore: 4801, FeedLimit: 5}} {
			v, err := s.ReadView(ctx, "p", 0, q)
			if err != nil {
				t.Fatal(name, err)
			}
			if v.Chain.Verified != wantVerified || (!wantVerified && (len(v.Feed) != 0 || v.Chain.Reason != ReasonDoesNotVerify)) {
				t.Fatalf("%s %+v: verified=%v rows=%d reason=%q", name, q, v.Chain.Verified, len(v.Feed), v.Chain.Reason)
			}
		}
	}
	check("intact", true)

	// A forged payload at row 3, far from the newest page and from the page before=100.
	var payload string
	if err := db.QueryRow(`SELECT payload FROM doomsday_audit WHERE profile = 'p' AND seq = 3`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE doomsday_audit SET payload = 'forged' WHERE profile = 'p' AND seq = 3`)
	check("forged row 3", false)
	exec(`UPDATE doomsday_audit SET payload = ? WHERE profile = 'p' AND seq = 3`, payload)
	check("row 3 restored", true)

	// A broken prev link at the first row of the newest page.
	var prev []byte
	if err := db.QueryRow(`SELECT prev FROM doomsday_audit WHERE profile = 'p' AND seq = 4801`).Scan(&prev); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE doomsday_audit SET prev = x'0102' WHERE profile = 'p' AND seq = 4801`)
	check("prev break at 4801", false)
	exec(`UPDATE doomsday_audit SET prev = ? WHERE profile = 'p' AND seq = 4801`, prev)
	check("row 4801 restored", true)

	// A gap in the middle.
	var kind, pl string
	var p, mac []byte
	if err := db.QueryRow(`SELECT kind, payload, prev, mac FROM doomsday_audit WHERE profile = 'p' AND seq = 2500`).Scan(&kind, &pl, &p, &mac); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM doomsday_audit WHERE profile = 'p' AND seq = 2500`)
	check("gap at 2500", false)
	exec(`INSERT INTO doomsday_audit(profile, generation, seq, kind, payload, prev, mac) VALUES ('p', ?, 2500, ?, ?, ?, ?)`, r.Generation, kind, pl, p, mac)
	check("gap closed", true)
}
