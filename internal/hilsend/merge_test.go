package hilsend

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// queueAs runs Guard as the test's agent and returns the output item of the
// one card it names.
func queueAs(t *testing.T, ctx context.Context, nodeType string, cfg map[string]interface{}) map[string]interface{} {
	t.Helper()
	out, handled, err := Guard(ctx, nodeType, workflow.NodeInput{}, cfg)
	if err != nil || !handled || len(out) != 1 || len(out[0].Items) == 0 {
		t.Fatalf("Guard %s handled=%v err=%v out=%+v", nodeType, handled, err, out)
	}
	return out[0].Items[0].JSON
}

func pendingCards(t *testing.T, st Store) []Payload {
	t.Helper()
	got, err := st.List(context.Background(), StatePending)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// An identical send that still waits is the same card: both callers are told
// its id, one Communications row follows it, and approving it sends once.
func TestIdenticalPendingSendsShareOneCard(t *testing.T) {
	asAgent(t)
	db := commsDB(t)
	st := Store{db, "p1"}
	first := queueAs(t, ctxFor(db), "comm.email_send", emailConfig())
	second := queueAs(t, ctxFor(db), "comm.email_send", emailConfig())
	if second["hil_id"] != first["hil_id"] {
		t.Fatalf("two cards for one send: %v and %v", first["hil_id"], second["hil_id"])
	}
	if _, ok := first["merged"]; ok || second["merged"] != true {
		t.Errorf("merged: first %v, second %v", first["merged"], second["merged"])
	}
	for _, j := range []map[string]interface{}{first, second} {
		if j["pending"] != true || j["success"] != false || j["state"] != "pending" || j["recipient"] != "ada@example.test" {
			t.Errorf("output = %+v", j)
		}
	}
	if got := pendingCards(t, st); len(got) != 1 {
		t.Fatalf("%d pending cards", len(got))
	}
	id := first["hil_id"].(string)
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM person_messages`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("%d Communications rows (%v)", rows, err)
	}
	f := &fakeRunner{}
	for i := 0; i < 2; i++ {
		if p, err := Resolve(context.Background(), st, id, true, Edit{}, false, f.run); err != nil || p.State != StateSent {
			t.Fatalf("approve %d: %+v %v", i, p, err)
		}
	}
	if len(f.calls) != 1 {
		t.Fatalf("the merged card sent %d times", len(f.calls))
	}
	if status, _, _, _, _, _ := commsRow(t, db, id); status != "sent" {
		t.Errorf("Communications row = %s", status)
	}
	// Sent is not waiting: the same send again is a new decision.
	third := queueAs(t, ctxFor(db), "comm.email_send", emailConfig())
	if third["hil_id"] == id || third["merged"] != nil {
		t.Errorf("merged into a sent card: %+v", third)
	}
}

// Anything that differs is a card of its own.
func TestASendThatDiffersGetsItsOwnCard(t *testing.T) {
	asAgent(t)
	db := commsDB(t)
	st := Store{db, "p1"}
	seen := map[string]string{queueAs(t, ctxFor(db), "comm.email_send", emailConfig())["hil_id"].(string): "base"}
	check := func(what string, j map[string]interface{}) {
		t.Helper()
		id := j["hil_id"].(string)
		if prior, ok := seen[id]; ok || j["merged"] != nil {
			t.Errorf("%s was merged into %q: %+v", what, prior, j)
		}
		seen[id] = what
	}
	file := filepath.Join(t.TempDir(), "a.pdf")
	if err := os.WriteFile(file, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		what, key string
		value     interface{}
	}{
		{"another body", "body", "Original body."},
		{"another subject", "subject", "Hello!"},
		{"a trailing space in the body", "body", "Original body "},
		{"another recipient", "to", "bob@example.test"},
		{"another account", "from", "other@example.test"},
		{"a cc", "cc", "carol@example.test"},
		{"a bcc", "bcc", "carol@example.test"},
		{"another connection", "credential_id", "conn-2"},
		{"an attachment", "attachments", []interface{}{file}},
		{"another option", "html", true},
	} {
		cfg := emailConfig()
		cfg[v.key] = v.value
		check(v.what, queueAs(t, ctxFor(db), "comm.email_send", cfg))
	}
	// The same path holding another file.
	if err := os.WriteFile(file, []byte("a longer file"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := emailConfig()
	cfg["attachments"] = []interface{}{file}
	check("a changed attachment", queueAs(t, ctxFor(db), "comm.email_send", cfg))
	// Another input item.
	out, _, err := Guard(ctxFor(db), "comm.email_send", workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"n": 1})}}, emailConfig())
	if err != nil {
		t.Fatal(err)
	}
	check("another input", out[0].Items[0].JSON)
	// Another profile.
	p2 := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p2")
	check("another profile", queueAs(t, p2, "comm.email_send", emailConfig()))
	// Another caller.
	t.Setenv(EnvConversation, "conv-43")
	check("another conversation", queueAs(t, ctxFor(db), "comm.email_send", emailConfig()))
	t.Setenv(EnvConversation, "conv-42")
	t.Setenv(EnvAgentRole, "support")
	check("another role", queueAs(t, ctxFor(db), "comm.email_send", emailConfig()))
	if got := len(pendingCards(t, st)); got != len(seen)-1 {
		t.Errorf("%d pending cards in p1, %d sends differ", got, len(seen)-1)
	}
}

// Only a card that still waits takes another caller: one that was rejected,
// is being sent, or whose text the operator changed does not.
func TestOnlyAnUnchangedWaitingCardIsMergedInto(t *testing.T) {
	asAgent(t)
	db := commsDB(t)
	st := Store{db, "p1"}
	queue := func() string { return queueAs(t, ctxFor(db), "comm.email_send", emailConfig())["hil_id"].(string) }

	rejected := queue()
	if _, err := Resolve(context.Background(), st, rejected, false, Edit{}, false, nil); err != nil {
		t.Fatal(err)
	}
	sending := queue()
	if sending == rejected {
		t.Fatal("merged into a rejected card")
	}
	var during string
	run := func(context.Context, Run) (json.RawMessage, error) {
		during = queue()
		return json.RawMessage(`{}`), nil
	}
	if _, err := Resolve(context.Background(), st, sending, true, Edit{}, false, run); err != nil {
		t.Fatal(err)
	}
	if during == sending {
		t.Error("merged into a card that was being sent")
	}
	edited := "The operator's words"
	if _, err := st.Edit(context.Background(), during, &edited, nil); err != nil {
		t.Fatal(err)
	}
	after := queue()
	if after == during {
		t.Error("merged into a card whose text differs")
	}
	// A card `hil list --suggest` annotated is still the same send.
	if _, err := db.Exec(`UPDATE hil_pending SET node_config = json_set(node_config, '$.suggestion', json('{"choice":"approve"}')) WHERE id = ?`, after); err != nil {
		t.Fatal(err)
	}
	if again := queue(); again != after {
		t.Errorf("a suggestion made the same send a second card: %s and %s", after, again)
	}
}

// A social node makes a card per recipient: a recipient listed again, in this
// run or a later one, keeps the one card, also once Communications saved the
// person (which the later card's lookup then finds).
func TestARepeatedSocialRecipientKeepsOneCard(t *testing.T) {
	asAgent(t)
	db := commsDB(t)
	st := Store{db, "p1"}
	cfg := func(urls ...string) map[string]interface{} {
		list := make([]interface{}, len(urls))
		for i, u := range urls {
			list[i] = u
		}
		return map[string]interface{}{"messageText": "Hi", "selectedListItems": list}
	}
	ada, bob := "https://www.instagram.com/ada/", "https://www.instagram.com/bob/"
	out, _, err := Guard(ctxFor(db), "instagram.send_dms", workflow.NodeInput{}, cfg(ada, bob, ada))
	if err != nil || len(out[0].Items) != 3 {
		t.Fatalf("%v %+v", err, out)
	}
	id := func(i int) interface{} { return out[0].Items[i].JSON["hil_id"] }
	if id(0) == id(1) || id(0) != id(2) {
		t.Fatalf("ids = %v %v %v", id(0), id(1), id(2))
	}
	later := queueAs(t, ctxFor(db), "instagram.send_dms", cfg(ada))
	if later["hil_id"] != id(0) {
		t.Errorf("a later run made a second card for ada: %+v", later)
	}
	if got := pendingCards(t, st); len(got) != 2 {
		t.Errorf("%d cards, want one per recipient", len(got))
	}
}

// Concurrent identical sends make one card.
func TestConcurrentIdenticalSendsMakeOneCard(t *testing.T) {
	asAgent(t)
	db := commsDB(t)
	st := Store{db, "p1"}
	const n = 12
	ids, errs := make([]string, n), make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			out, _, err := Guard(ctxFor(db), "comm.email_send", workflow.NodeInput{}, emailConfig())
			if errs[i] = err; err == nil {
				ids[i] = out[0].Items[0].JSON["hil_id"].(string)
			}
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range ids {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Fatalf("caller %d: id %q (first %q) err %v", i, ids[i], ids[0], errs[i])
		}
	}
	if got := pendingCards(t, st); len(got) != 1 {
		t.Fatalf("%d cards", len(got))
	}
}

// A held secret's references are bound to one card, so such sends never share.
func TestSendsHoldingASecretAreNotMerged(t *testing.T) {
	f := newHeldFix(t, anyHost())
	a, b := f.hold(t, "key is "+heldValue), f.hold(t, "key is "+heldValue)
	if a == b {
		t.Fatal("two sends holding a secret share a card")
	}
}
