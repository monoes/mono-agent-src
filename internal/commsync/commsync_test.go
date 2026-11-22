package commsync

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := storage.NewDatabase(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.DB.Close() })
	return d.DB
}

func msg(t *testing.T, db *sql.DB, ref string) (status, direction, body, personID string) {
	t.Helper()
	if err := db.QueryRow(`SELECT status, direction, COALESCE(body,''), person_id FROM person_messages WHERE hil_ref = ?`, ref).
		Scan(&status, &direction, &body, &personID); err != nil {
		t.Fatalf("message of %s: %v", ref, err)
	}
	return
}

func statusOf(t *testing.T, db *sql.DB, ref string) string {
	t.Helper()
	s, _, _, _ := msg(t, db, ref)
	return s
}

func TestQueueDMCreatesPersonAndPendingRow(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	res, err := Queue(ctx, db, Item{Profile: "default", HilRef: "h1", Source: "instagram", Action: "dm", Account: Account{Platform: "instagram", Handle: "acme"},
		Recipient: Recipient{Name: "Ada", ProfileURL: "https://www.instagram.com/ada/"}, Text: "Hi Ada"})
	if err != nil {
		t.Fatal(err)
	}
	status, dir, body, pid := msg(t, db, "h1")
	if status != "pending_approval" || dir != "outbound" || body != "Hi Ada" || pid != res.PersonID {
		t.Fatalf("row = %s %s %q %s", status, dir, body, pid)
	}
	var user, platform, kind, category string
	db.QueryRow(`SELECT platform_username, platform, kind, COALESCE(category,'') FROM people WHERE id = ?`, pid).Scan(&user, &platform, &kind, &category)
	if user != "ada" || platform != "INSTAGRAM" || kind != "person" || category != "" {
		t.Fatalf("person = %s %s %s cat=%q", user, platform, kind, category)
	}
	var ext string
	db.QueryRow(`SELECT external_id FROM person_messages WHERE hil_ref = 'h1'`).Scan(&ext)
	if ext != "hil:h1" {
		t.Fatalf("external_id = %s", ext)
	}
}

func TestQueueIsIdempotent(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	it := Item{Profile: "default", HilRef: "h1", Source: "x", Action: "dm", Recipient: Recipient{Handle: "ada"}, Text: "a"}
	a, _ := Queue(ctx, db, it)
	b, err := Queue(ctx, db, it)
	if err != nil || a.MessageID != b.MessageID {
		t.Fatalf("second queue: %v %+v %+v", err, a, b)
	}
	var n, p int
	db.QueryRow(`SELECT COUNT(*) FROM person_messages`).Scan(&n)
	db.QueryRow(`SELECT COUNT(*) FROM people`).Scan(&p)
	if n != 1 || p != 1 {
		t.Fatalf("rows %d people %d", n, p)
	}
}

func TestQueueCompanyRecipient(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	res, err := Queue(ctx, db, Item{Profile: "default", HilRef: "h2", Source: "linkedin", Action: "dm",
		Recipient: Recipient{Name: "Acme Inc", ProfileURL: "https://www.linkedin.com/company/acme-inc/"}, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	var kind, user string
	db.QueryRow(`SELECT kind, platform_username FROM people WHERE id = ?`, res.PersonID).Scan(&kind, &user)
	if kind != "company" || user != "acme-inc" {
		t.Fatalf("company = %s %s", kind, user)
	}
}

func TestQueuePostLinksOwnAccount(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	res, err := Queue(ctx, db, Item{Profile: "default", HilRef: "h3", Source: "x", Action: "post", Account: Account{Platform: "x", Handle: "acme"}, Text: "launch!"})
	if err != nil || !res.Own {
		t.Fatalf("%v %+v", err, res)
	}
	var user, details, kind string
	db.QueryRow(`SELECT platform_username, profile_details, kind FROM people WHERE id = ?`, res.PersonID).Scan(&user, &details, &kind)
	if user != "acme" || kind != "person" || details != `{"own_account":true}` {
		t.Fatalf("own = %s %s %s", user, kind, details)
	}
	res2, _ := Queue(ctx, db, Item{Profile: "default", HilRef: "h4", Source: "x", Action: "post", Account: Account{Platform: "x", Handle: "acme"}, Text: "again"})
	if res2.PersonID != res.PersonID {
		t.Fatal("own account duplicated")
	}
	res3, err := Queue(ctx, db, Item{Profile: "default", HilRef: "h5", Source: "linkedin", Action: "post", Text: "x"})
	if err != nil || res3.PersonID == "" {
		t.Fatalf("no handle: %v", err)
	}
}

func TestQueueKeepsExistingPersonFields(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	db.Exec(`INSERT INTO people (id, platform_username, platform, full_name, category, introduction, profile_id) VALUES ('p1','ada','INSTAGRAM','Ada L','approved','draft dm','default')`)
	res, _ := Queue(ctx, db, Item{Profile: "default", HilRef: "h6", Source: "instagram", Action: "dm", Recipient: Recipient{Handle: "ada", Name: "Other"}, Text: "x"})
	if res.PersonID != "p1" {
		t.Fatalf("matched %s", res.PersonID)
	}
	var name, cat, intro string
	db.QueryRow(`SELECT full_name, category, introduction FROM people WHERE id='p1'`).Scan(&name, &cat, &intro)
	if name != "Ada L" || cat != "approved" || intro != "draft dm" {
		t.Fatalf("overwritten: %s %s %s", name, cat, intro)
	}
}

func TestStatusLifecycleAndGuard(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	Queue(ctx, db, Item{Profile: "default", HilRef: "h", Source: "x", Action: "dm", Recipient: Recipient{Handle: "a"}, Text: "t"})
	for _, to := range []Status{Approved, Sending, Sent} {
		if err := SetStatus(ctx, db, "h", to, Detail{}); err != nil {
			t.Fatalf("-> %s: %v", to, err)
		}
	}
	if s := statusOf(t, db, "h"); s != "sent" {
		t.Fatal(s)
	}
	var sentAt sql.NullTime
	db.QueryRow(`SELECT sent_at FROM person_messages WHERE hil_ref='h'`).Scan(&sentAt)
	if !sentAt.Valid {
		t.Error("sent_at not set")
	}
	for _, to := range []Status{PendingApproval, Approved, Failed, Rejected} {
		if err := SetStatus(ctx, db, "h", to, Detail{}); !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("sent -> %s: %v", to, err)
		}
	}
	if err := SetStatus(ctx, db, "h", Sent, Detail{}); err != nil {
		t.Errorf("sent -> sent should be a no-op: %v", err)
	}
	if s := statusOf(t, db, "h"); s != "sent" {
		t.Fatal(s)
	}
	if err := SetStatus(ctx, db, "nope", Approved, Detail{}); err != nil {
		t.Fatal(err)
	}
}

func TestStatusRejectedFailedExpired(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	for _, ref := range []string{"r", "f", "e"} {
		Queue(ctx, db, Item{Profile: "default", HilRef: ref, Source: "x", Action: "dm", Recipient: Recipient{Handle: ref}, Text: "t"})
	}
	SetStatus(ctx, db, "r", Rejected, Detail{})
	SetStatus(ctx, db, "e", Expired, Detail{})
	SetStatus(ctx, db, "f", Approved, Detail{})
	SetStatus(ctx, db, "f", Sending, Detail{})
	if err := SetStatus(ctx, db, "f", Failed, Detail{Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]string{"r": "rejected", "e": "expired", "f": "failed"} {
		if s := statusOf(t, db, ref); s != want {
			t.Errorf("%s = %s", ref, s)
		}
	}
	var meta string
	db.QueryRow(`SELECT metadata FROM person_messages WHERE hil_ref='f'`).Scan(&meta)
	if !strings.Contains(meta, `"error":"boom"`) {
		t.Errorf("metadata = %s", meta)
	}
	if err := SetStatus(ctx, db, "r", Approved, Detail{}); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("rejected -> approved: %v", err)
	}
	if err := SetStatus(ctx, db, "e", Sending, Detail{}); !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("expired -> sending: %v", err)
	}
}

func TestEditedTextUpdatesBody(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	Queue(ctx, db, Item{Profile: "default", HilRef: "h", Source: "x", Action: "post", Text: "old", TextField: "text"})
	if err := SetStatus(ctx, db, "h", Approved, Detail{Edited: map[string]interface{}{"text": "new"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, body, _ := msg(t, db, "h"); body != "new" {
		t.Fatalf("body = %q", body)
	}
}

func queueTargeted(t *testing.T, db *sql.DB, exec, ref string) {
	t.Helper()
	ctx := context.Background()
	db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status) VALUES (?, ?, 'w', 'hil', 'Approve', 'approved')`, ref, exec)
	Queue(ctx, db, Item{Profile: "default", HilRef: ref, Source: "x", Action: "post", Text: "t", Meta: map[string]interface{}{
		"workflow": map[string]interface{}{"target_nodes": []string{"pub"}}}})
	SetStatus(ctx, db, ref, Approved, Detail{})
}

func TestNodeHooksSendOnceAndFail(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	queueTargeted(t, db, "ex1", "a")
	NodeStarted(ctx, db, "ex1", "other")
	if s := statusOf(t, db, "a"); s != "approved" {
		t.Fatalf("unrelated node moved it: %s", s)
	}
	NodeStarted(ctx, db, "ex1", "pub")
	if s := statusOf(t, db, "a"); s != "sending" {
		t.Fatalf("%s", s)
	}
	NodeFinished(ctx, db, "ex1", "pub", nil)
	NodeFinished(ctx, db, "ex1", "pub", errors.New("late failure")) // sent stays sent
	if s := statusOf(t, db, "a"); s != "sent" {
		t.Fatalf("%s", s)
	}

	queueTargeted(t, db, "ex2", "c")
	NodeStarted(ctx, db, "ex2", "pub")
	NodeFinished(ctx, db, "ex2", "pub", errors.New("429"))
	if s := statusOf(t, db, "c"); s != "failed" {
		t.Fatalf("%s", s)
	}
}

func TestBulkRejectForExecution(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	db.Exec(`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, status) VALUES ('z', 'ex9', 'w', 'hil', 'n', 'rejected')`)
	Queue(ctx, db, Item{Profile: "default", HilRef: "z", Source: "x", Action: "post", Text: "t"})
	if err := SetStatusForExecution(ctx, db, "ex9", Rejected); err != nil {
		t.Fatal(err)
	}
	if s := statusOf(t, db, "z"); s != "rejected" {
		t.Fatal(s)
	}
}

func TestLinkForFindsThePersonOfAQueuedItem(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	res, err := Queue(ctx, db, Item{Profile: "default", HilRef: "h9", Source: "instagram", Action: "dm", Account: Account{Platform: "instagram", Handle: "acme"},
		Recipient: Recipient{Name: "Ada", ProfileURL: "https://www.instagram.com/ada/"}, Text: "Hi"})
	if err != nil {
		t.Fatal(err)
	}
	pid, name, ok := LinkFor(ctx, db, "h9")
	if !ok || pid != res.PersonID || name != "Ada" {
		t.Fatalf("LinkFor = %q %q %v", pid, name, ok)
	}
	if _, _, ok := LinkFor(ctx, db, "nothing-outbound"); ok {
		t.Fatal("an item with no row must have no link")
	}
}

func peopleOf(t *testing.T, db *sql.DB, pid string) (platform, user string) {
	t.Helper()
	if err := db.QueryRow(`SELECT platform, platform_username FROM people WHERE id = ?`, pid).Scan(&platform, &user); err != nil {
		t.Fatal(err)
	}
	return
}

func TestQueueUsesTheAdapterUsernameAndUpperCasePlatform(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	res, err := Queue(ctx, db, Item{Profile: "default", HilRef: "u1", Source: "linkedin", Action: "dm", Account: Account{Platform: "linkedin", Handle: "acme"},
		Recipient: Recipient{ProfileURL: "https://www.linkedin.com/in/alice/details/experience/"}, Text: "Hi"})
	if err != nil {
		t.Fatal(err)
	}
	if platform, user := peopleOf(t, db, res.PersonID); platform != "LINKEDIN" || user != "alice" {
		t.Fatalf("person = %s/%s, want LINKEDIN/alice", platform, user)
	}
	// A row a node writer made earlier is found, not duplicated.
	if _, err := db.Exec(`INSERT INTO people (id, platform_username, platform, profile_id) VALUES ('bob1','bob','LINKEDIN','default')`); err != nil {
		t.Fatal(err)
	}
	res, err = Queue(ctx, db, Item{Profile: "default", HilRef: "u2", Source: "linkedin", Action: "dm", Account: Account{Platform: "linkedin", Handle: "acme"},
		Recipient: Recipient{ProfileURL: "https://www.linkedin.com/in/bob/"}, Text: "Hi"})
	if err != nil || res.PersonID != "bob1" {
		t.Fatalf("existing LINKEDIN row not reused: %q, %v", res.PersonID, err)
	}
}

func TestQueueMailtoHandleIsTheAddress(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	res, err := Queue(ctx, db, Item{Profile: "default", HilRef: "m1", Source: "email", Action: "email", Account: Account{Platform: "email", Handle: "me@x.test"},
		Recipient: Recipient{ProfileURL: "mailto:Sam@Example.com"}, Text: "Hi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, user := peopleOf(t, db, res.PersonID); user != "sam@example.com" {
		t.Fatalf("username = %q, want sam@example.com", user)
	}
}

func TestQueueRefusesARecipientKnownOnlyByName(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	_, err := Queue(ctx, db, Item{Profile: "default", HilRef: "n1", Source: "linkedin", Action: "dm", Account: Account{Platform: "linkedin", Handle: "acme"},
		Recipient: Recipient{Name: "Sam Lee"}, Text: "Hi"})
	if err == nil {
		t.Fatal("a free-text name must not become a username")
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM people WHERE platform_username = 'Sam Lee'`).Scan(&n)
	if n != 0 {
		t.Fatal("a person row was created from a name")
	}
}

func TestUpsertPersonConcurrentCreatorsShareOneRow(t *testing.T) {
	db, ctx := testDB(t), context.Background()
	const workers = 8
	ids := make([]string, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			ids[i], errs[i] = upsertPerson(ctx, db, personSpec{profile: "default", platform: "linkedin", username: "zed", kind: KindPerson})
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range ids {
		if errs[i] != nil || ids[i] != ids[0] {
			t.Fatalf("worker %d: id %q err %v; first id %q", i, ids[i], errs[i], ids[0])
		}
	}
}
