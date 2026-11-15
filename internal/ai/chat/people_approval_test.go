package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/peoplereview"
)

// upsert_person is the chat's and the MCP server's (person_upsert): a model
// drives it, so it never approves a person for outreach, even in a process
// that is the user's own; it may queue and reject, and a new text for an
// approved person goes back to the queue (#831 S5).
func TestUpsertPersonNeverApproves(t *testing.T) {
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "", "default")
	ctx := context.Background()
	up := func(args string) error {
		_, err := mt.ExecuteContext(ctx, "upsert_person", args)
		return err
	}
	row := func() (id, category, intro string) {
		t.Helper()
		db.DB.QueryRow(`SELECT id, COALESCE(category,''), COALESCE(introduction,'') FROM people
			WHERE platform = 'LINKEDIN' AND platform_username = 'ada' AND profile_id = 'default'`).Scan(&id, &category, &intro)
		return
	}

	for _, cat := range []string{"approved", " Approved "} {
		if err := up(`{"platform":"linkedin","platform_username":"ada","category":"` + cat + `","introduction":"Hi Ada"}`); err == nil ||
			!strings.Contains(err.Error(), "only the user approves") {
			t.Fatalf("a new person %q: %v", cat, err)
		}
	}
	if id, _, _ := row(); id != "" {
		t.Fatal("a refused upsert saved ada")
	}

	if err := up(`{"platform":"linkedin","platform_username":"ada","category":"pending_approval","introduction":"Hi Ada"}`); err != nil {
		t.Fatalf("queueing: %v", err)
	}
	id, _, _ := row()
	if _, err := peoplereview.Approve(ctx, db.DB, "default", id, ""); err != nil {
		t.Fatal(err)
	}
	if err := up(`{"platform":"linkedin","platform_username":"ada","category":"approved","introduction":"Hi Ada"}`); err != nil {
		t.Fatalf("re-saving the approved person as they are: %v", err)
	}
	if err := up(`{"platform":"linkedin","platform_username":"ada","category":"approved","introduction":"Hi Ada, buy now"}`); err == nil {
		t.Fatal("approved a new text")
	}
	if _, cat, intro := row(); cat != peoplereview.Approved || intro != "Hi Ada" {
		t.Fatalf("a refused upsert changed ada: %q %q", cat, intro)
	}
	if err := up(`{"platform":"linkedin","platform_username":"ada","introduction":"Hi Ada, buy now"}`); err != nil {
		t.Fatal(err)
	}
	if _, cat, intro := row(); cat != peoplereview.Pending || intro != "Hi Ada, buy now" {
		t.Fatalf("a new text for an approved person: %q %q, want back in the queue", cat, intro)
	}
	if err := up(`{"platform":"linkedin","platform_username":"ada","category":"rejected"}`); err != nil {
		t.Fatalf("rejecting: %v", err)
	}
	if _, cat, _ := row(); cat != peoplereview.Rejected {
		t.Fatalf("ada = %q, want rejected", cat)
	}
}
