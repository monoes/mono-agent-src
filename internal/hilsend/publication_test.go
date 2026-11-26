package hilsend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/workflow"
)

// sentEmail queues an email as an agent and has the user approve it; the
// send's node returns result. It returns the item's id.
func sentEmail(t *testing.T, db *sql.DB, result string) string {
	t.Helper()
	id := queueEmail(t, db)
	run := func(context.Context, Run) (json.RawMessage, error) { return json.RawMessage(result), nil }
	if _, err := Resolve(context.Background(), Store{db, "p1"}, id, true, Edit{}, false, run); err != nil {
		t.Fatal(err)
	}
	return id
}

// A publication from whatever is not the operator is the one a send the
// user approved made: that send is sent, on the publication's platform,
// returned its remote id when it has one (an entry without one is refused
// then), and registers one publication only, though a retry of that same
// publication passes. Nothing is asked while approvals of sends are off.
func TestClaimPublicationApproval(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	ctx := context.Background()
	sent := sentEmail(t, db, `{"main":[{"json":{"message_id":"m-42"}}]}`)
	// Queued after the first was sent: an identical send that still waits shares its card (merge.go).
	pending := queueEmail(t, db)
	p, err := (Store{db, "p1"}).Get(ctx, sent)
	if err != nil {
		t.Fatal(err)
	}
	platform := p.Account.Platform
	if platform == "" {
		platform = p.Channel
	}
	entry := func(remoteID string) publication.Entry {
		return publication.Entry{Platform: platform, Kind: "post", Body: "hi", RemoteID: remoteID}
	}

	for _, approval := range []string{"", pending, "missing"} {
		if _, err := ClaimPublicationApproval(ctx, db, "p1", approval, entry("m-42")); !errors.Is(err, ErrPublicationNeedsApproval) {
			t.Errorf("approval %q, not sent: %v", approval, err)
		}
	}
	if _, err := ClaimPublicationApproval(ctx, db, "p2", sent, entry("m-42")); !errors.Is(err, ErrPublicationNeedsApproval) {
		t.Errorf("another profile's send: %v", err)
	}
	var mismatch *PublicationMismatchError
	other := entry("m-42")
	other.Platform = "x"
	if _, err := ClaimPublicationApproval(ctx, db, "p1", sent, other); !errors.As(err, &mismatch) || mismatch.What != "platform" {
		t.Errorf("another platform: %v", err)
	}
	for _, remote := range []string{"m-7", ""} {
		if _, err := ClaimPublicationApproval(ctx, db, "p1", sent, entry(remote)); !errors.As(err, &mismatch) || mismatch.What != "remote id" {
			t.Errorf("remote id %q where the send returned m-42: %v", remote, err)
		}
	}
	claim, err := ClaimPublicationApproval(ctx, db, "p1", sent, entry("m-42"))
	if err != nil || claim == nil {
		t.Fatalf("the send's own publication: %v", err)
	}
	// The same publication again: its registration is idempotent, so it passes.
	if again, err := ClaimPublicationApproval(ctx, db, "p1", sent, entry("m-42")); err != nil || again != nil {
		t.Errorf("a retry of the same publication: %v %v", again, err)
	}
	keyed := entry("m-42")
	keyed.IdempotencyKey = "another"
	if _, err := ClaimPublicationApproval(ctx, db, "p1", sent, keyed); !errors.Is(err, ErrPublicationApprovalUsed) {
		t.Errorf("another publication with the approval: %v", err)
	}
	// A registration that failed gives it back.
	claim.Release(ctx, db)
	if claim, err = ClaimPublicationApproval(ctx, db, "p1", sent, keyed); err != nil || claim == nil {
		t.Errorf("after a release: %v", err)
	}
	if p, err := (Store{db, "p1"}).Get(ctx, sent); err != nil || p.State != StateSent {
		t.Errorf("the claimed send: %+v %v", p, err)
	}

	if _, err := db.Exec(`INSERT INTO configs (name, config_data) VALUES ('coder_mode', '{"sendApproval":false}')`); err != nil {
		t.Fatal(err)
	}
	if claim, err := ClaimPublicationApproval(ctx, db, "p1", "", publication.Entry{Platform: "anything"}); err != nil || claim != nil {
		t.Errorf("approvals of sends off: %v %v", claim, err)
	}
}

// The run that performs an approved send spends the approval on the
// publication it records itself: a later registration passes for that same
// publication only. Only that run, holding the approval's proof, spends it.
func TestASendsOwnRegistrationSpendsItsApproval(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	ctx := context.Background()
	own := publication.Entry{Platform: "email", Kind: "post", Body: "hi", RemoteID: "m-42"}
	id := queueEmail(t, db)
	// A process without the proof spends nothing (were it to, the send's own
	// publication would be refused below).
	t.Setenv(EnvApprovalID, id)
	t.Setenv(EnvApprovalProof, "forged")
	ConsumeForSend(ctx, db, "p1", []publication.Entry{{Platform: "email", Kind: "post", Body: "x", RemoteID: "forged"}})
	run := func(_ context.Context, r Run) (json.RawMessage, error) {
		t.Setenv(EnvApprovalID, r.ApprovalID)
		t.Setenv(EnvApprovalProof, r.Proof)
		ConsumeForSend(ctx, db, "p1", []publication.Entry{own})
		return json.RawMessage(`{"main":[{"json":{"message_id":"m-42"}}]}`), nil
	}
	if _, err := Resolve(ctx, Store{db, "p1"}, id, true, Edit{}, false, run); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvApprovalID, "")
	t.Setenv(EnvApprovalProof, "")
	p, _ := (Store{db, "p1"}).Get(ctx, id)
	own.Platform = p.Account.Platform
	if own.Platform == "" {
		own.Platform = p.Channel
	}
	another := own
	another.IdempotencyKey = "a-second-post"
	if _, err := ClaimPublicationApproval(ctx, db, "p1", id, another); !errors.Is(err, ErrPublicationApprovalUsed) {
		t.Errorf("a second publication with a send's approval it spent itself: %v", err)
	}
	if claim, err := ClaimPublicationApproval(ctx, db, "p1", id, own); err != nil || claim != nil {
		t.Errorf("the send's own publication again: %v %v", claim, err)
	}
}

// A service or comm node's send is on the platform its publication names
// (service.devto: devto), not on the node family its card shows: its
// publication, registered by hand when tracking failed, passes.
func TestAServiceSendsPublicationIsOnItsPlatform(t *testing.T) {
	asAgent(t)
	db := testDB(t)
	ctx := context.Background()
	out, handled, err := Guard(ctxFor(db), "service.devto", workflow.NodeInput{},
		map[string]interface{}{"operation": "publish_article", "title": "Hello", "body_markdown": "Hi all"})
	if err != nil || !handled {
		t.Fatalf("Guard handled=%v err=%v", handled, err)
	}
	id := out[0].Items[0].JSON["hil_id"].(string)
	run := func(context.Context, Run) (json.RawMessage, error) {
		return json.RawMessage(`{"main":[{"json":{"id":"4242"}}]}`), nil
	}
	if _, err := Resolve(ctx, Store{db, "p1"}, id, true, Edit{}, false, run); err != nil {
		t.Fatal(err)
	}
	var mismatch *PublicationMismatchError
	if _, err := ClaimPublicationApproval(ctx, db, "p1", id, publication.Entry{Platform: "hashnode", Kind: "article", RemoteID: "4242"}); !errors.As(err, &mismatch) {
		t.Errorf("another platform: %v", err)
	}
	if claim, err := ClaimPublicationApproval(ctx, db, "p1", id, publication.Entry{Platform: "devto", Kind: "article", RemoteID: "4242"}); err != nil || claim == nil {
		t.Errorf("the devto send's article: %v %v", claim, err)
	}
}

func TestSentRemoteID(t *testing.T) {
	for result, want := range map[string]string{
		`{"main":[{"json":{"remote_id":"r","id":"i"}}]}`:   "r",
		`{"main":[{"json":{"id":12345}}]}`:                 "12345",
		`{"main":[{"json":{}},{"json":{"uri":"at://x"}}]}`: "at://x",
		`{"error":[{"json":{"id":"e"}}]}`:                  "",
		`"plain text"`:                                     "",
	} {
		var v any
		_ = json.Unmarshal([]byte(result), &v)
		if got := sentRemoteID(v); got != want {
			t.Errorf("%s: %q, want %q", result, got, want)
		}
	}
}
