package peoplenodes

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// people.save finds a row stored under another spelling of the platform
// (people import wrote it lower-case) and updates it: no twin, the row keeps
// its spelling, and the review guard sees its approval.
func TestPeopleSaveUpdatesTheRowOfAnotherPlatformSpelling(t *testing.T) {
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	db := setupTestDB(t)
	defer db.Close()
	SetGlobalPeopleDB(db)
	if _, err := db.Exec(`INSERT INTO people (id, platform_username, platform, profile_id, category, introduction)
		VALUES ('old', 'ada', 'linkedin', 'default', 'approved', 'Hi Ada')`); err != nil {
		t.Fatal(err)
	}
	node := &PeopleSaveNode{}
	base := vault.ContextWithProfileID(context.Background(), "default")
	save := func(ctx context.Context, fields map[string]interface{}) error {
		item := map[string]interface{}{"profile_url": "https://www.linkedin.com/in/ada/", "platform": "linkedin"}
		for k, v := range fields {
			item[k] = v
		}
		_, err := node.Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(item)}}, map[string]interface{}{})
		return err
	}

	if err := save(base, map[string]interface{}{"full_name": "Ada L"}); err != nil {
		t.Fatal(err)
	}
	var n int
	var name, plat string
	db.QueryRow(`SELECT COUNT(*), MAX(full_name), MAX(platform) FROM people WHERE platform_username = 'ada'`).Scan(&n, &name, &plat)
	if n != 1 || name != "Ada L" || plat != "linkedin" {
		t.Fatalf("rows=%d name=%q platform=%q, want 1 Ada L linkedin", n, name, plat)
	}

	// An AI run cannot approve a different text through the other spelling.
	ai := workflow.WithAIAuthored(base)
	err := save(ai, map[string]interface{}{"category": "approved", "introduction": "Hi Ada, changed"})
	if err == nil || !strings.Contains(err.Error(), "only the user approves") {
		t.Fatalf("AI run approving through another spelling: %v", err)
	}
	if err := save(ai, map[string]interface{}{"introduction": "Hi Ada, changed"}); err != nil {
		t.Fatal(err)
	}
	var cat string
	db.QueryRow(`SELECT category FROM people WHERE platform_username = 'ada'`).Scan(&cat)
	if cat != "pending_approval" {
		t.Fatalf("a new text for an approved person: category %q, want pending_approval", cat)
	}
}
