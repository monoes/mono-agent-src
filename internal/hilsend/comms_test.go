package hilsend

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

func commsDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	testdb.AddDefaultProfile(t, d.DB)
	t.Cleanup(func() { d.DB.Close() })
	return d.DB
}

func commsRow(t *testing.T, db *sql.DB, hil string) (status, direction, source, body, user, platform string) {
	t.Helper()
	err := db.QueryRow(`SELECT pm.status, pm.direction, pm.source, COALESCE(pm.body,''), p.platform_username, p.platform
		FROM person_messages pm JOIN people p ON p.id = pm.person_id WHERE pm.hil_ref = ?`, hil).
		Scan(&status, &direction, &source, &body, &user, &platform)
	if err != nil {
		t.Fatalf("communications row of %s: %v", hil, err)
	}
	return
}

func TestSendQueueCreatesRowAndFollowsToSent(t *testing.T) {
	asAgent(t)
	db := commsDB(t)
	id := queueEmail(t, db)
	status, dir, source, body, user, platform := commsRow(t, db, id)
	if status != "pending_approval" || dir != "outbound" || source != "email" || body != "Original body" ||
		user != "ada@example.test" || platform != "email" {
		t.Fatalf("queued = %s %s %s %q %s %s", status, dir, source, body, user, platform)
	}

	st := Store{db, "p1"}
	f := &fakeRunner{}
	edited := "Edited body"
	if _, err := Resolve(context.Background(), st, id, true, Edit{Text: &edited}, false, f.run); err != nil {
		t.Fatal(err)
	}
	status, _, _, body, _, _ = commsRow(t, db, id)
	if status != "sent" || body != edited || len(f.calls) != 1 {
		t.Fatalf("after approve: %s %q calls=%d", status, body, len(f.calls))
	}
	// A second approve and a late reject change nothing.
	Resolve(context.Background(), st, id, true, Edit{}, false, f.run)
	Resolve(context.Background(), st, id, false, Edit{}, false, f.run)
	if status, _, _, _, _, _ = commsRow(t, db, id); status != "sent" || len(f.calls) != 1 {
		t.Fatalf("status %s calls %d", status, len(f.calls))
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM person_messages`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d rows", n)
	}
}

func TestSendRejectAndFailure(t *testing.T) {
	asAgent(t)
	db := commsDB(t)
	st := Store{db, "p1"}
	f := &fakeRunner{}

	rejected := queueEmail(t, db)
	if _, err := Resolve(context.Background(), st, rejected, false, Edit{}, false, f.run); err != nil {
		t.Fatal(err)
	}
	if status, _, _, _, _, _ := commsRow(t, db, rejected); status != "rejected" || len(f.calls) != 0 {
		t.Fatalf("rejected: %s calls %d", status, len(f.calls))
	}

	failed := queueEmail(t, db)
	boom := &fakeRunner{err: errors.New("smtp refused")}
	Resolve(context.Background(), st, failed, true, Edit{}, false, boom.run)
	if status, _, _, _, _, _ := commsRow(t, db, failed); status != "failed" {
		t.Fatalf("failed: %s", status)
	}
	var meta string
	db.QueryRow(`SELECT metadata FROM person_messages WHERE hil_ref = ?`, failed).Scan(&meta)
	if !contains(meta, "smtp refused") {
		t.Fatalf("error missing: %s", meta)
	}
}

func TestSendEditWhilePendingUpdatesBody(t *testing.T) {
	asAgent(t)
	db := commsDB(t)
	id := queueEmail(t, db)
	text := "New text"
	if _, err := (Store{db, "p1"}).Edit(context.Background(), id, &text, nil); err != nil {
		t.Fatal(err)
	}
	if status, _, _, body, _, _ := commsRow(t, db, id); status != "pending_approval" || body != text {
		t.Fatalf("%s %q", status, body)
	}
}

func TestSendPostLinksOwnAccountAndDMSavesRecipient(t *testing.T) {
	asAgent(t)
	db := commsDB(t)
	out, handled, err := Guard(ctxFor(db), "x.publish_post", workflow.NodeInput{}, map[string]interface{}{"account": "acme", "text": "Launch"})
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	id := out[0].Items[0].JSON["hil_id"].(string)
	status, _, source, body, user, platform := commsRow(t, db, id)
	if status != "pending_approval" || source != "x" || body != "Launch" || user != "acme" || platform != "x" {
		t.Fatalf("post = %s %s %q %s %s", status, source, body, user, platform)
	}
	var details string
	db.QueryRow(`SELECT p.profile_details FROM people p JOIN person_messages pm ON pm.person_id = p.id WHERE pm.hil_ref = ?`, id).Scan(&details)
	if details != `{"own_account":true}` {
		t.Fatalf("details = %s", details)
	}

	out, _, err = Guard(ctxFor(db), "linkedin.send_dms", workflow.NodeInput{}, map[string]interface{}{
		"messageText": "Hi", "selectedListItems": []interface{}{map[string]interface{}{"name": "Acme", "profileUrl": "https://www.linkedin.com/company/acme/"}}})
	if err != nil {
		t.Fatal(err)
	}
	id = out[0].Items[0].JSON["hil_id"].(string)
	_, _, _, _, user, platform = commsRow(t, db, id)
	var kind string
	db.QueryRow(`SELECT p.kind FROM people p JOIN person_messages pm ON pm.person_id = p.id WHERE pm.hil_ref = ?`, id).Scan(&kind)
	if user != "acme" || platform != "linkedin" || kind != "company" {
		t.Fatalf("company dm = %s %s %s", user, platform, kind)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
