package hil

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// `hil approve|reject --if-digest` decides a workflow item only while it is as
// `hil list` showed it.
func TestDecisionsNamingAnOldDigestAreRefused(t *testing.T) {
	cfg, db, _ := hilSuggestEnv(t, false, nil)
	seedPending(t, db, "alpha", "bravo")
	items, err := runHIL(t, cfg, "list")
	if err != nil || len(items) != 2 || items[0].Digest == "" || items[0].Digest == items[1].Digest {
		t.Fatalf("list: %v %+v", err, items)
	}
	id, old := items[0].ID, items[0].Digest
	var ro, ed string
	if err := db.QueryRow(`SELECT readonly_data, editable_data FROM hil_pending WHERE id = ?`, id).Scan(&ro, &ed); err != nil || hilsend.Digest(ro, ed, "") != old {
		t.Fatalf("digest of the row: %v", err)
	}
	if _, err := db.Exec(`UPDATE hil_pending SET editable_data = '{"caption":"changed"}' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	status := func() (s string) {
		_ = db.QueryRow(`SELECT status FROM hil_pending WHERE id = ?`, id).Scan(&s)
		return s
	}
	for _, verb := range []string{"approve", "reject"} {
		_, err := runHIL(t, cfg, verb, id, "--if-digest", old)
		if exitCodeFor(err) != 3 || status() != "pending" {
			t.Fatalf("%s with the old digest: %v, status %s", verb, err, status())
		}
	}
	items, err = runHIL(t, cfg, "list")
	if err != nil || items[0].Digest == old {
		t.Fatalf("the digest did not move: %v %+v", err, items)
	}
	if _, err := runHIL(t, cfg, "approve", id, "--if-digest", items[0].Digest); err != nil || status() != "approved" {
		t.Fatalf("current digest: %v, status %s", err, status())
	}
	if _, err := runHIL(t, cfg, "reject", items[1].ID, "--if-digest", items[1].Digest); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, err := runHIL(t, cfg, "approve", id, "--if-digest", items[0].Digest); exitCodeFor(err) != 2 {
		t.Fatalf("a decided item: %v", err)
	}
}

// `hil list --redact`, which fills the chat's cards, hides reply link tokens
// from the user's own process too, in a send's card as well, and marks the item.
func TestHILListRedactHidesReplyTokensAndMarksTheItem(t *testing.T) {
	t.Setenv("MONOAGENT_PUBLIC_URL", "http://127.0.0.1:19724")
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	cfg := &testConfig{DBPath: newHILTestDB(t), JSONOutput: true, ProfileID: "default"}
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	resume, err := workflow.ExecutionResumeURL("ex-r")
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id, ro, ed, nc string) {
		if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
			VALUES (?, 'ex-r', 'wf-r', 'n', 'Review', 'pending', ?, ?, ?, 'default')`, id, ro, ed, nc); err != nil {
			t.Fatal(err)
		}
	}
	insert("h1", `{"link":"`+resume+`"}`, `{}`, `{"outbound":false}`)
	insert("h2", `{"kind":"send","action":"dm","recipient":{"name":"Ada"}}`, `{"text":"Answer at `+resume+`"}`, `{"kind":"send","textField":"text"}`)
	insert("h3", `{"note":"plain"}`, `{}`, `{"outbound":false}`)
	// A send with nothing to hide is not marked, whatever else its card holds.
	insert("h4", `{"kind":"send","action":"email","recipient":{"name":"ada@example.test","profileUrl":"https://example.test/ada"},"account":{"platform":"email"},`+
		`"attachments":[{"kind":"file","name":"a.pdf","path":"/tmp/a.pdf","size":12345678}],"details":[{"key":"cc","value":"bob@example.test"}],"createdAt":"2026-11-27T10:00:00Z"}`,
		`{"text":"See https://example.test/offer/6f1b2c3d4e5f60718293a4b5c6d7e8f9","subject":"Offer"}`, `{"kind":"send","textField":"body","subjectKey":"subject"}`)

	list := func(args ...string) string {
		var err error
		out := captureStdout(t, func() {
			cmd := NewCommand(testDeps(cfg))
			cmd.SetArgs(append([]string{"list"}, args...))
			err = cmd.Execute()
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	token := resume[strings.LastIndex(resume, "/")+1:]
	if !strings.Contains(list(), token) {
		t.Fatal("the plain list hides the user's own link")
	}
	out := list("--redact")
	if strings.Contains(out, token) {
		t.Fatalf("the token is in the redacted list:\n%s", out)
	}
	var items []hilItem
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 4 {
		t.Fatalf("%v: %s", err, out)
	}
	for _, it := range items {
		if it.Redacted != (it.ID == "h1" || it.ID == "h2") {
			t.Errorf("%s: redacted = %v", it.ID, it.Redacted)
		}
		if it.ID == "h2" && (it.Send == nil || !strings.Contains(it.Send.Text, workflow.ReplyTokenHidden)) {
			t.Errorf("send card %+v", it.Send)
		}
	}
}

// Browser submit, site and pairing items are checked and then decided in two
// steps, so a chat card, which names a digest, cannot approve them: `hil list`
// marks them and approve refuses.
func TestItemsDecidedInTwoStepsCannotBeApprovedFromACard(t *testing.T) {
	cfg, db, _ := hilSuggestEnv(t, false, nil)
	kinds := map[string]string{
		"submit": `{"kind":"` + browserctl.SubmitKind + `"}`,
		"site":   `{"kind":"` + browserctl.SiteKind + `"}`,
		"pair":   `{"kind":"` + extension.PairCardKind + `","claim":"c1"}`,
		"plain":  `{}`,
	}
	for id, nc := range kinds {
		if _, err := db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status, readonly_data, editable_data, node_config, profile_id)
			VALUES (?, ?, '', 'n', 'Review', 'pending', '{}', '{}', ?, 'default')`, id, "x-"+id, nc); err != nil {
			t.Fatal(err)
		}
	}
	items, err := runHIL(t, cfg, "list")
	if err != nil || len(items) != 4 {
		t.Fatalf("%v %+v", err, items)
	}
	for _, it := range items {
		if it.NoChatApprove != (it.ID != "plain") {
			t.Errorf("%s: no_chat_approve = %v", it.ID, it.NoChatApprove)
		}
		if it.ID == "plain" {
			continue
		}
		_, err := runHIL(t, cfg, "approve", it.ID, "--if-digest", it.Digest)
		if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "Communications") {
			t.Errorf("%s: %v (exit %d)", it.ID, err, exitCodeFor(err))
		}
		var status string
		if err := db.QueryRow(`SELECT status FROM hil_pending WHERE id = ?`, it.ID).Scan(&status); err != nil || status != "pending" {
			t.Errorf("%s: %s %v", it.ID, status, err)
		}
	}
}
