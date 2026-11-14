package orgdecide

import (
	"context"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// A card an older version wrote holds a granted secret's value in its review
// data: the org's decider is given it scrubbed (#1594), as every other reader.
func TestAnOldCardsReviewTextIsScrubbedBeforeTheOrgReadsIt(t *testing.T) {
	keyring.MockInit()
	s := newTestService(t, &fakeOrg{}, &scriptedDecider{})
	db := s.DB
	const value = "vsecret-777"
	if _, err := db.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p', 'p')`); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db), "p")
	if _, err := secrets.Add(ctx, db, "p", "secret", "vs", map[string]string{"secret": value}, "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := secrets.SetAgentAccessGrant(ctx, db, "p", secrets.AccessGrant{AnyHost: true}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO workflows (id, name, profile_id) VALUES ('wf', 'Publish', 'p')`,
		`INSERT INTO workflow_executions (id, workflow_id, status, trigger_type, trigger_data, profile_id) VALUES ('e-org', 'wf', 'WAITING', 'org_tool', '{"org":{"name":"growth","role":"writer","automation":"publish"}}', 'p')`,
		`INSERT INTO hil_pending (id, execution_id, workflow_id, node_id, node_name, readonly_data, node_config, profile_id, created_at) VALUES ('hil-org', 'e-org', 'wf', 'n', 'Review post', '{"text":"key ` + value + `"}', '{"outbound":false}', 'p', datetime('now','-1 day'))`,
		// the card is an older one: the scrub's cutoff and bound lie past it
		`INSERT OR REPLACE INTO settings (key, value) VALUES ('hilscrub.cutoff', datetime('now','+1 day'))`,
		`INSERT OR REPLACE INTO settings (key, value) VALUES ('hilscrub.cutoff.rowid', '1000000')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	items, err := PendingHIL(ctx, db, "p", "growth", nil)
	if err != nil || len(items) != 1 {
		t.Fatalf("%+v %v", items, err)
	}
	if strings.Contains(items[0].Text, value) {
		t.Errorf("the org was given the secret: %q", items[0].Text)
	}
}
