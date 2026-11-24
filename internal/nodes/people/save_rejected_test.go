package peoplenodes

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A re-scrape of someone the user rejected must not put them back in the
// approval queue (R16-D1); only the user's review commands change that.
func TestPeopleSaveKeepsRejected(t *testing.T) {
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
	db := setupTestDB(t)
	defer db.Close()
	SetGlobalPeopleDB(db)
	node := &PeopleSaveNode{}
	ctx := vault.ContextWithProfileID(context.Background(), "default")
	save := func(c context.Context, fields map[string]interface{}) {
		t.Helper()
		item := map[string]interface{}{"profile_url": "https://www.linkedin.com/in/rita/", "platform": "linkedin"}
		for k, v := range fields {
			item[k] = v
		}
		if _, err := node.Execute(c, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(item)}}, map[string]interface{}{}); err != nil {
			t.Fatal(err)
		}
	}
	cat := func() (c string) {
		db.QueryRow(`SELECT COALESCE(category,'') FROM people WHERE platform_username = 'rita'`).Scan(&c)
		return
	}
	save(ctx, map[string]interface{}{"full_name": "Rita"})
	if _, err := db.Exec(`UPDATE people SET category = 'rejected' WHERE platform_username = 'rita'`); err != nil {
		t.Fatal(err)
	}
	save(ctx, map[string]interface{}{"full_name": "Rita R", "category": "pending_approval", "introduction": "Hi Rita"})
	if c := cat(); c != "rejected" {
		t.Fatalf("user run re-scrape: category %q, want rejected", c)
	}
	save(workflow.WithAIAuthored(ctx), map[string]interface{}{"introduction": "Hi Rita, new"})
	if c := cat(); c != "rejected" {
		t.Fatalf("AI run new text: category %q, want rejected", c)
	}
}
