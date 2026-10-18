package publicationnodes

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

func TestRegisterBatchUsesEachItemAndScopesProfile(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithProfileID(vault.ContextWithDB(context.Background(), db.DB), "default")
	in := workflow.NodeInput{WorkflowID: "workflow", ExecutionID: "execution", NodeID: "register", Items: []workflow.Item{
		workflow.NewItem(map[string]interface{}{"text": `{"published":"json-looking text"}`, "id": "remote-a", "profile_id": "other"}),
		workflow.NewItem(map[string]interface{}{"text": "second", "id": "remote-b"}),
		workflow.NewItem(map[string]interface{}{"text": "failed", "success": false}),
	}}
	config := map[string]interface{}{"platform": "custom", "kind": "post", "body": "{{ $json.text }}", "remote_id": "{{ $json.id }}", "media": `["https://example.com/published.png"]`}
	out, err := (&RegisterNode{}).Execute(ctx, in, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Items) != 2 {
		t.Fatalf("outputs: %+v", out)
	}
	if _, err = (&RegisterNode{}).Execute(ctx, in, config); err != nil {
		t.Fatal(err)
	}
	entries, err := publication.NewStore(db.DB, "default").List(ctx, publication.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries: %+v", entries)
	}
	bodies := map[string]bool{}
	for _, e := range entries {
		bodies[e.Body] = true
		if len(e.Media) != 1 || e.Media[0] != "https://example.com/published.png" {
			t.Fatalf("media JSON config: %+v", e.Media)
		}
		if e.ProfileID != "default" || e.ExecutionID != "execution" {
			t.Fatalf("wrong source: %+v", e)
		}
	}
	if !bodies[`{"published":"json-looking text"}`] || !bodies["second"] {
		t.Fatalf("bodies: %v", bodies)
	}
	others, err := publication.NewStore(db.DB, "other").List(ctx, publication.Filter{})
	if err != nil || len(others) != 0 {
		t.Fatalf("other profile: %v %v", others, err)
	}
}
func TestRegisterRequiresDatabase(t *testing.T) {
	if _, err := (&RegisterNode{}).Execute(context.Background(), workflow.NodeInput{}, nil); err == nil {
		t.Fatal("expected error")
	}
}

func TestRegisterPreservesUpstreamTemplateText(t *testing.T) {
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	ctx := vault.ContextWithDB(context.Background(), db.DB)
	literal := `Published Go template: {{ $json.secret }}`
	out, err := (&RegisterNode{}).Execute(ctx, workflow.NodeInput{Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"platform": "blog", "kind": "post", "body": literal})}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Items) != 1 || out[0].Items[0].JSON["body"] != literal {
		t.Fatalf("literal content changed: %+v", out)
	}
}
