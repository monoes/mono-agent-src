package people

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// claimDraft is a compare-and-swap: of two callers racing for one draft, only
// the first wins, so a draft is never sent twice.
func TestClaimDraftOnlyOneWins(t *testing.T) {
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m-draft", "EXT", "outbound", "", "draft", `{"connection_id":"outlook"}`)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := claimDraft(db.DB, "m-draft"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := claimDraft(db.DB, "m-draft"); err == nil {
		t.Fatal("second claim succeeded; the draft would be sent twice")
	}
}

// stubGuard installs an exec guard that records each call and handles it
// when handle is set, answering with a queued item; the default guard is
// restored when the test ends.
func stubGuard(t *testing.T, handle bool, calls *[]map[string]interface{}) {
	t.Helper()
	workflow.SetExecGuard(func(_ context.Context, nodeType string, _ workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, bool, error) {
		c := map[string]interface{}{"nodeType": nodeType}
		for k, v := range config {
			c[k] = v
		}
		*calls = append(*calls, c)
		if !handle {
			return nil, false, nil
		}
		return nodeOutput(map[string]interface{}{"pending": true, "hil_id": "hil-1"}), true, nil
	})
	t.Cleanup(func() { workflow.SetExecGuard(nil) })
}

// The commands that send an email through service.outlook_mail (compose,
// compose --reply-to, reply, send-draft) pass the exec guard first, as a
// workflow step and `node run` do. A send the guard handles never reaches
// the node: the command says it waits for approval, and records nothing as
// sent (a draft stays a draft). They called the node directly, so an
// agent's shell could send mail with no approval card.
func TestPeopleMessageSendsPassTheExecGuard(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		op   string
	}{
		{"compose", []string{"messages", "compose", "p1", "--connection", "outlook", "--subject", "Hi", "--body", "Hello Alice"}, "send_message"},
		{"compose --reply-to", []string{"messages", "compose", "p1", "--connection", "outlook", "--body", "Thanks", "--reply-to", "m-in"}, "reply"},
		{"reply", []string{"messages", "reply", "m-in", "--connection", "outlook", "--body", "Thanks"}, "reply"},
		{"send-draft", []string{"messages", "send-draft", "m-draft"}, "send_draft"},
	} {
		t.Run(c.name, func(t *testing.T) {
			asOperator(t) // send-draft is the user's; the guard is what's tested here
			dbPath := newMessagesCLITestDB(t)
			seedPersonMessage(t, dbPath, "m-in", "EXT-IN", "inbound", "alice@x.com", "read", `{"connection_id":"outlook"}`)
			seedPersonMessage(t, dbPath, "m-draft", "EXT-DRAFT", "outbound", "", "draft", `{"connection_id":"outlook"}`)
			runs := 0
			stubOutlookNode(t, func(*sql.DB, map[string]interface{}) ([]workflow.NodeOutput, error) {
				runs++
				return nodeOutput(map[string]interface{}{"status": "sent", "message_id": "EXT-NEW"}), nil
			})
			var calls []map[string]interface{}
			stubGuard(t, true, &calls)

			_, err := runPeople(t, &testConfig{DBPath: dbPath, ProfileID: "default"}, c.args...)
			if err == nil || !strings.Contains(err.Error(), "NOT sent") || !strings.Contains(err.Error(), "hil-1") {
				t.Fatalf("err = %v, want it to say the send waits for approval", err)
			}
			if runs != 0 {
				t.Fatalf("the node ran %d time(s) for a send the guard held", runs)
			}
			if len(calls) != 1 || calls[0]["nodeType"] != "service.outlook_mail" || calls[0]["operation"] != c.op || calls[0]["credential_id"] != "outlook" {
				t.Fatalf("guard calls = %v, want one for service.outlook_mail %s with the connection", calls, c.op)
			}
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var rows int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM person_messages`).Scan(&rows); err != nil || rows != 2 {
				t.Fatalf("%d message rows (%v), want only the 2 seeded: nothing recorded as sent", rows, err)
			}
			if m := fetchPersonMessage(t, dbPath, "m-draft"); m == nil || m.Status != "draft" {
				t.Fatalf("the draft is %+v, want it still a draft", m)
			}
		})
	}
}

// A send the guard leaves alone (the user's own) runs the node once, as
// before.
func TestPeopleMessageSendTheGuardLeavesRuns(t *testing.T) {
	asOperator(t)
	dbPath := newMessagesCLITestDB(t)
	runs := 0
	stubOutlookNode(t, func(*sql.DB, map[string]interface{}) ([]workflow.NodeOutput, error) {
		runs++
		return nodeOutput(map[string]interface{}{"status": "sent", "message_id": "EXT-NEW"}), nil
	})
	var calls []map[string]interface{}
	stubGuard(t, false, &calls)
	if _, err := runPeople(t, &testConfig{DBPath: dbPath, ProfileID: "default"},
		"messages", "compose", "p1", "--connection", "outlook", "--subject", "Hi", "--body", "Hello Alice"); err != nil {
		t.Fatal(err)
	}
	if runs != 1 || len(calls) != 1 {
		t.Fatalf("node runs = %d, guard calls = %d, want 1 and 1", runs, len(calls))
	}
}

// An agent's send-draft is refused (exit 3, operator_only): the draft's
// recipients, subject and text live in the mailbox, so its card would be
// approved blind. The node never runs and the draft stays a draft.
func TestSendDraftIsTheUsers(t *testing.T) {
	asOperator(t)
	t.Setenv("CLAUDECODE", "1")
	dbPath := newMessagesCLITestDB(t)
	seedPersonMessage(t, dbPath, "m-draft", "EXT-DRAFT", "outbound", "", "draft", `{"connection_id":"outlook"}`)
	runs := 0
	stubOutlookNode(t, func(*sql.DB, map[string]interface{}) ([]workflow.NodeOutput, error) {
		runs++
		return nodeOutput(map[string]interface{}{"status": "sent", "message_id": "EXT-NEW"}), nil
	})
	_, err := runPeople(t, &testConfig{DBPath: dbPath, ProfileID: "default"}, "messages", "send-draft", "m-draft")
	var ce *clicore.Error
	var fields interface{ JSONErrorFields() map[string]any }
	if !errors.As(err, &ce) || ce.Code != 3 || !errors.As(err, &fields) || fields.JSONErrorFields()["code"] != "operator_only" {
		t.Fatalf("an agent's send-draft: %v, want exit 3 operator_only", err)
	}
	if runs != 0 {
		t.Fatalf("the node ran %d time(s)", runs)
	}
	if m := fetchPersonMessage(t, dbPath, "m-draft"); m == nil || m.Status != "draft" {
		t.Fatalf("the draft is %+v, want it still a draft", m)
	}
}

// The card of an agent's compose shows every copy the send carries and the
// connection it goes out through, pinned by id: with two Outlook
// connections, the one this run chose (the active one), not whichever an
// approval would pick. A compose with a bcc read as a send to its To alone.
func TestPeopleMessageCardShowsCopiesAndPinsTheConnection(t *testing.T) {
	asOperator(t)
	t.Setenv(hilsend.EnvAgentContext, "coder")
	workflow.SetExecGuard(hilsend.Guard)
	t.Cleanup(func() { workflow.SetExecGuard(nil) })
	dbPath := newMessagesCLITestDB(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	store := connections.NewStore(db.DB)
	for _, c := range []*connections.Connection{
		{ID: "conn-old", Platform: "outlook", Method: "oauth", Status: "expired", ProfileID: "default", Data: map[string]interface{}{"email": "me@x.com"}},
		{ID: "conn-new", Platform: "outlook", Method: "oauth", Status: "active", ProfileID: "default", Data: map[string]interface{}{"email": "me@x.com"}},
	} {
		if err := store.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	runs := 0
	stubOutlookNode(t, func(*sql.DB, map[string]interface{}) ([]workflow.NodeOutput, error) {
		runs++
		return nil, nil
	})
	_, err = runPeople(t, &testConfig{DBPath: dbPath, ProfileID: "default"}, "messages", "compose", "p1", "--connection", "outlook",
		"--subject", "Hi", "--body", "Hello Alice", "--cc", "eve@example.test", "--bcc", "mallory@example.test")
	if err == nil || !strings.Contains(err.Error(), "NOT sent") || runs != 0 {
		t.Fatalf("compose: %v (node runs %d), want it held", err, runs)
	}
	db, err = storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cards, err := hilsend.Store{DB: db.DB, Profile: "default"}.List(context.Background())
	if err != nil || len(cards) != 1 {
		t.Fatalf("cards: %v %+v", err, cards)
	}
	got := map[string]string{}
	for _, d := range cards[0].Details {
		got[d.Key] = d.Value
	}
	if got["cc"] != "eve@example.test" || got["bcc"] != "mallory@example.test" || got["bodyType"] != "html" || got["connection"] != "conn-new" {
		t.Fatalf("card details %v, want the cc, the bcc, the body type and conn-new", got)
	}
}
