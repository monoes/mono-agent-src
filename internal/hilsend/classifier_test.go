package hilsend

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// fakeClassifier sends for app.fake's "send" op, and its "reply" op whose
// text is an option of a collection param.
type fakeClassifier struct{}

func (fakeClassifier) Classify(nodeType string, config map[string]interface{}) (Classified, bool) {
	if nodeType != "app.fake" {
		return Classified{}, false
	}
	switch config["operation"] {
	case "send":
		return Classified{Action: ActionDM, Channel: "fake", To: []string{"to"}, Text: []string{"text"}, PerItem: []string{"to", "text"}}, true
	case "reply":
		return Classified{Action: ActionComment, Channel: "fake", To: []string{"ticket"}, Text: []string{"updateFields.publicReply"}}, true
	case "raw":
		return Classified{Action: ActionPost, Channel: "fake", ToUnset: "whoever its undeclared fields (zz) reach",
			Op: "fake thing/update", Show: []string{"updateFields.zz"}, PerItem: []string{"updateFields"}}, true
	case "remind":
		return Classified{Action: ActionEmail, Channel: "fake", To: []string{"email", "invoiceId"},
			ToLabel: map[string]string{"invoiceId": "the client of invoice"}, ToUnset: "the invoice's client"}, true
	}
	return Classified{}, false
}

func (fakeClassifier) SendsSome(nodeType string) bool { return nodeType == "app.fake" }

// Node types the table doesn't know are sends when the installed classifier
// says so, judged on the operation the run resolved, or, ahead of the run,
// on one only the run resolves; without a classifier they send as before.
func TestClassifierExtendsTheTable(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	SetClassifier(nil)
	if _, handled, _ := Guard(ctxFor(db), "app.fake", workflow.NodeInput{}, map[string]interface{}{"operation": "send"}); handled {
		t.Fatal("held with no classifier installed")
	}
	SetClassifier(fakeClassifier{})
	t.Cleanup(func() { SetClassifier(nil) })
	out, handled, err := Guard(ctxFor(db), "app.fake", workflow.NodeInput{Items: []workflow.Item{
		workflow.NewItem(map[string]interface{}{"who": "+15550100"}),
		workflow.NewItem(map[string]interface{}{"who": "+15550101"}),
	}}, map[string]interface{}{"operation": "send", "to": "{{ $json.who }}", "text": "Hi"})
	if !handled || err != nil || len(out[0].Items) != 2 {
		t.Fatalf("handled=%v err=%v out=%+v", handled, err, out)
	}
	for i, want := range []string{"+15550100", "+15550101"} {
		p, err := (Store{db, "p1"}).Get(ctxFor(db), out[0].Items[i].JSON["hil_id"].(string))
		if err != nil || p.Recipient.Name != want || p.Channel != "fake" || p.Account.Platform != "fake" || p.Action != ActionDM {
			t.Fatalf("item %d = %+v, %v", i, p, err)
		}
	}
	for cfg, want := range map[string]bool{"send": true, "list": false, "{{ $json.op }}": true} {
		if got := MaySend("app.fake", map[string]interface{}{"operation": cfg}); got != want {
			t.Errorf("MaySend(%q) = %v", cfg, got)
		}
	}
	if MaySend("app.other", map[string]interface{}{"operation": "{{ $json.op }}"}) {
		t.Error("a node type that never sends may send")
	}
}

// A send's text can be an option of a collection param (an API package's
// "updateFields.publicReply"): the card shows it, and the approved run gets
// the text the user approved there, with the collection's other options.
func TestASendsTextInACollection(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	SetClassifier(fakeClassifier{})
	t.Cleanup(func() { SetClassifier(nil) })
	out, handled, err := Guard(ctxFor(db), "app.fake", workflow.NodeInput{}, map[string]interface{}{
		"operation": "reply", "ticket": "T-1", "updateFields": map[string]interface{}{"publicReply": "Fixed it", "status": "solved"}})
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	st := Store{db, "p1"}
	id := out[0].Items[0].JSON["hil_id"].(string)
	if p, err := st.Get(ctxFor(db), id); err != nil || p.Text != "Fixed it" || p.Recipient.Name != "T-1" {
		t.Fatalf("card = %+v, %v", p, err)
	}
	f := &fakeRunner{}
	edited := "Fixed it, sorry for the wait"
	if _, err := Resolve(context.Background(), st, id, true, Edit{Text: &edited}, false, f.run); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("runs = %d", len(f.calls))
	}
	if uf, _ := f.calls[0].Config["updateFields"].(map[string]interface{}); uf["publicReply"] != edited || uf["status"] != "solved" {
		t.Errorf("the approved run's updateFields = %v", f.calls[0].Config["updateFields"])
	}
}

// A send whose credential is written into its config can't wait for
// approval, which keeps no credentials: it is refused, with how to fix it,
// not queued as a card that can only fail. One resolved from a saved
// connection waits as before.
func TestAnInlineCredentialIsRefusedNotQueued(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	SetClassifier(fakeClassifier{})
	t.Cleanup(func() { SetClassifier(nil) })
	inline := map[string]interface{}{"operation": "send", "to": "+15550100", "text": "Hi",
		"credential": map[string]interface{}{"api_key": "k"}}
	if _, handled, err := Guard(ctxFor(db), "app.fake", workflow.NodeInput{}, inline); !handled || err == nil ||
		!strings.Contains(err.Error(), "connect save") || !strings.Contains(err.Error(), "credential_id") {
		t.Fatalf("inline: handled=%v err=%v", handled, err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hil_pending`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("queued %d item(s), %v", n, err)
	}
	saved := map[string]interface{}{"operation": "send", "to": "+15550100", "text": "Hi", "credential_id": "fake",
		"api_key": "k", "credential": map[string]interface{}{"api_key": "k"}}
	if out, handled, err := Guard(ctxFor(db), "app.fake", workflow.NodeInput{}, saved); !handled || err != nil || len(out[0].Items) != 1 {
		t.Fatalf("saved: handled=%v err=%v", handled, err)
	}
}

// A send whose recipient param is not an address shows what it is (the
// client of an invoice, not "inv9"), and one with none set shows who it
// goes to; neither is looked up as a person, however one is written. An
// address shows as it is.
func TestTheCardNamesARecipientThatIsNoAddress(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	SetClassifier(fakeClassifier{})
	t.Cleanup(func() { SetClassifier(nil) })
	if _, err := db.Exec(`INSERT INTO people (id, platform_username, platform, full_name, contact_details) VALUES
		('p1', 'inv', 'EMAIL', 'Not a client', 'the client of invoice inv9'), ('p2', 'ada', 'EMAIL', 'Ada Lovelace', 'ada@example.test')`); err != nil {
		t.Fatal(err)
	}
	st := Store{db, "p1"}
	for _, c := range []struct {
		config         map[string]interface{}
		name, personID string
	}{
		{map[string]interface{}{"operation": "remind", "invoiceId": "inv9"}, "the client of invoice inv9", ""},
		{map[string]interface{}{"operation": "remind"}, "the invoice's client", ""},
		// The op names its recipient: a "user" in the config is no guess for it.
		{map[string]interface{}{"operation": "remind", "user": "bob"}, "the invoice's client", ""},
		{map[string]interface{}{"operation": "remind", "email": "ada@example.test", "invoiceId": "inv9"}, "Ada Lovelace", "p2"},
	} {
		out, handled, err := Guard(ctxFor(db), "app.fake", workflow.NodeInput{}, c.config)
		if !handled || err != nil {
			t.Fatalf("%v: handled=%v err=%v", c.config, handled, err)
		}
		p, err := st.Get(ctxFor(db), out[0].Items[0].JSON["hil_id"].(string))
		if err != nil || p.Recipient.Name != c.name || p.Recipient.PersonID != c.personID {
			t.Errorf("%v: recipient %+v, %v; want %q (person %q)", c.config, p.Recipient, err, c.name, c.personID)
		}
	}
}

// A card for keys an op doesn't declare shows the op and each key's value
// as that item sends it, and has no message to edit: an edit would change
// nothing the approved run sends, so it is refused.
func TestAnUndeclaredKeysCardShowsThemAndCannotBeEdited(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	SetClassifier(fakeClassifier{})
	t.Cleanup(func() { SetClassifier(nil) })
	out, handled, err := Guard(ctxFor(db), "app.fake", workflow.NodeInput{Items: []workflow.Item{
		workflow.NewItem(map[string]interface{}{"v": "first"}), workflow.NewItem(map[string]interface{}{"v": "second"}),
	}}, map[string]interface{}{"operation": "raw", "updateFields": map[string]interface{}{"zz": "{{ $json.v }}", "name": "N"}})
	if !handled || err != nil || len(out[0].Items) != 2 {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	st := Store{db, "p1"}
	for i, want := range []string{"first", "second"} {
		p, err := st.Get(ctxFor(db), out[0].Items[i].JSON["hil_id"].(string))
		if err != nil || p.Text != "fake thing/update with fields it doesn't declare:\nupdateFields.zz: \""+want+"\"" || !p.Recipient.Label {
			t.Errorf("item %d: text %q, recipient %+v, %v", i, p.Text, p.Recipient, err)
		}
	}
	id := out[0].Items[0].JSON["hil_id"].(string)
	if p, _ := st.Get(ctxFor(db), id); !p.TextReadOnly {
		t.Errorf("the card's text is not marked read-only: %+v", p)
	}
	text := "something else"
	if _, err := st.Edit(ctxFor(db), id, &text, nil); !errors.Is(err, ErrNoText) {
		t.Errorf("edit: %v", err)
	}

	// Approving with an edit fails, sends nothing and leaves the card
	// pending: the edit would have been dropped. Without one it sends.
	f := &fakeRunner{}
	if _, err := Resolve(context.Background(), st, id, true, Edit{Text: &text}, false, f.run); !errors.Is(err, ErrNoText) || len(f.calls) != 0 {
		t.Fatalf("approve with an edit: %v, %d run(s)", err, len(f.calls))
	}
	if p, _ := st.Get(ctxFor(db), id); p.State != StatePending {
		t.Fatalf("after a refused edit the card is %s", p.State)
	}
	if _, err := Resolve(context.Background(), st, id, true, Edit{}, false, f.run); err != nil || len(f.calls) != 1 {
		t.Fatalf("approve: %v, %d run(s)", err, len(f.calls))
	}
	// Once decided, an edit with the approve reports the state, as before.
	if p, err := Resolve(context.Background(), st, id, true, Edit{Text: &text}, false, f.run); err != nil || p.State == StatePending || len(f.calls) != 1 {
		t.Errorf("approve again with an edit: %s, %v, %d run(s)", p.State, err, len(f.calls))
	}
	// A card with a message is editable.
	o, _, _ := Guard(ctxFor(db), "app.fake", workflow.NodeInput{}, map[string]interface{}{"operation": "send", "to": "+15550100", "text": "Hi"})
	if p, _ := st.Get(ctxFor(db), o[0].Items[0].JSON["hil_id"].(string)); p.TextReadOnly {
		t.Errorf("a message card is marked read-only")
	}
}
