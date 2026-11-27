package hilsend

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func rowDigest(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var ro, ed, nc string
	if err := db.QueryRow(`SELECT readonly_data, editable_data, node_config FROM hil_pending WHERE id = ?`, id).Scan(&ro, &ed, &nc); err != nil {
		t.Fatal(err)
	}
	return Digest(ro, ed, nc)
}

// A card drawn before an edit cannot decide the item: its old text is not
// written over the newer one, nothing runs, and the item stays pending.
func TestADecisionNamingAnOldCardIsRefused(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	id := queueEmail(t, db)
	asOperator(t)
	st, run, ctx := Store{db, "p1"}, &fakeRunner{}, context.Background()
	old := rowDigest(t, db, id)
	newer := "Newer body: the price is $499"
	if _, err := st.Edit(ctx, id, &newer, nil); err != nil {
		t.Fatal(err)
	}
	stale := "Original body"
	for name, approve := range map[string]bool{"approve": true, "reject": false} {
		edit := Edit{IfDigest: old}
		if approve {
			edit.Text = &stale
		}
		if _, err := Resolve(ctx, st, id, approve, edit, false, run.run); !errors.Is(err, ErrChanged) {
			t.Fatalf("%s with the old card: %v", name, err)
		}
	}
	p, _ := st.Get(ctx, id)
	if p.State != StatePending || p.Text != newer || len(run.calls) != 0 {
		t.Fatalf("state %s, text %q, %d runs", p.State, p.Text, len(run.calls))
	}
	// the current card decides, with its edit
	mine := "Mine"
	p, err := Resolve(ctx, st, id, true, Edit{Text: &mine, IfDigest: rowDigest(t, db, id)}, false, run.run)
	if err != nil || p.State != StateSent || p.Text != mine || len(run.calls) != 1 || run.calls[0].Config["body"] != mine {
		t.Fatalf("%+v %v %+v", p, err, run.calls)
	}
}

// The check is part of the claim: a change that lands between the read and the
// write is not approved.
func TestAChangeUnderTheClaimRefusesIt(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	id := queueEmail(t, db)
	asOperator(t)
	st, run, ctx := Store{db, "p1"}, &fakeRunner{}, context.Background()
	digest := rowDigest(t, db, id)
	betweenReadAndWrite = func() {
		betweenReadAndWrite = nil
		if _, err := db.Exec(`UPDATE hil_pending SET editable_data = '{"text":"swapped","subject":"Hello"}' WHERE id = ?`, id); err != nil {
			t.Error(err)
		}
	}
	defer func() { betweenReadAndWrite = nil }()
	if _, err := Resolve(ctx, st, id, true, Edit{IfDigest: digest}, false, run.run); !errors.Is(err, ErrChanged) {
		t.Fatalf("%v", err)
	}
	if p, _ := st.Get(ctx, id); p.State != StatePending || p.Text != "swapped" || len(run.calls) != 0 {
		t.Fatalf("%+v, %d runs", p, len(run.calls))
	}
}

// What the digest covers: the shown data and the replay, not the bookkeeping a
// list or a claim writes beside them.
func TestDigestCoversContentOnly(t *testing.T) {
	nc := `{"kind":"send","nodeType":"comm.slack","config":{"text":"a","channel":"#x"},"input":null,"textField":"text"}`
	base := Digest("{}", `{"text":"a"}`, nc)
	for name, same := range map[string]string{
		"suggestion": Digest("{}", `{"text":"a"}`, nc[:len(nc)-1]+`,"suggestion":{"choice":"approve"}}`),
		"key order":  Digest("{}", `{"text":"a"}`, `{"textField":"text","input":null,"config":{"channel":"#x","text":"a"},"nodeType":"comm.slack","kind":"send"}`),
	} {
		if same != base {
			t.Errorf("%s changed the digest", name)
		}
	}
	for name, other := range map[string]string{
		"readonly": Digest(`{"x":1}`, `{"text":"a"}`, nc),
		"editable": Digest("{}", `{"text":"b"}`, nc),
		"replay":   Digest("{}", `{"text":"a"}`, `{"kind":"send","nodeType":"comm.slack","config":{"text":"a","channel":"#y"},"textField":"text"}`),
		"shifted":  Digest(`{}{"text":"a"}`, ``, nc),
	} {
		if other == base {
			t.Errorf("%s did not change the digest", name)
		}
	}
}

// A decision made from a card (it names a digest) of a send decided elsewhere
// is an error, not a quiet answer with the item's state: the caller must not
// report it as its own, and its edit is said to be dropped.
func TestADecisionFromACardOfADecidedSendIsAnError(t *testing.T) {
	for _, status := range []string{"sent", "rejected", "sending", "failed"} {
		asAgent(t)
		db := testDB(t)
		id := queueEmail(t, db)
		asOperator(t)
		st, run, ctx := Store{db, "p1"}, &fakeRunner{}, context.Background()
		digest := rowDigest(t, db, id)
		if _, err := db.Exec(`UPDATE hil_pending SET status = ? WHERE id = ?`, status, id); err != nil {
			t.Fatal(err)
		}
		mine := "my edit"
		for name, c := range map[string]struct {
			approve bool
			edit    Edit
		}{
			"approve":           {true, Edit{IfDigest: digest}},
			"approve with edit": {true, Edit{IfDigest: digest, Text: &mine}},
			"reject":            {false, Edit{IfDigest: digest}},
		} {
			p, err := Resolve(ctx, st, id, c.approve, c.edit, false, run.run)
			if !errors.Is(err, ErrDecided) || p.State != stateOf(status) {
				t.Errorf("%s of a %s send: state %q, err %v", name, status, p.State, err)
			}
		}
		if p, _ := st.Get(ctx, id); p.Text != "Original body" || len(run.calls) != 0 {
			t.Errorf("%s: text %q, %d runs", status, p.Text, len(run.calls))
		}
		// Without a digest, deciding again stays a no-op that reports the state.
		if p, err := Resolve(ctx, st, id, true, Edit{}, false, run.run); err != nil || p.State != stateOf(status) {
			t.Errorf("%s without a digest: %q %v", status, p.State, err)
		}
	}
}

// The item decided between the read and the claim is refused the same way.
func TestASendDecidedUnderTheClaimIsAnError(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	id := queueEmail(t, db)
	asOperator(t)
	st, run, ctx := Store{db, "p1"}, &fakeRunner{}, context.Background()
	digest := rowDigest(t, db, id)
	betweenReadAndWrite = func() {
		betweenReadAndWrite = nil
		if _, err := db.Exec(`UPDATE hil_pending SET status = 'rejected' WHERE id = ?`, id); err != nil {
			t.Error(err)
		}
	}
	defer func() { betweenReadAndWrite = nil }()
	if p, err := Resolve(ctx, st, id, true, Edit{IfDigest: digest}, false, run.run); !errors.Is(err, ErrDecided) || p.State != StateRejected || len(run.calls) != 0 {
		t.Fatalf("%q %v", p.State, err)
	}
}
