package hil

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

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
	if err := json.Unmarshal([]byte(out), &items); err != nil || len(items) != 3 {
		t.Fatalf("%v: %s", err, out)
	}
	for _, it := range items {
		if it.Redacted != (it.ID != "h3") {
			t.Errorf("%s: redacted = %v", it.ID, it.Redacted)
		}
		if it.ID == "h2" && (it.Send == nil || !strings.Contains(it.Send.Text, workflow.ReplyTokenHidden)) {
			t.Errorf("send card %+v", it.Send)
		}
	}
}
