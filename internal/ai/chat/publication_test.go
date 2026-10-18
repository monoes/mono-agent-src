package chat

import (
	"context"
	"encoding/json"
	"github.com/monoes/mono-agent/internal/publication"
	"testing"
)

func TestPublicationAssistantToolsAndInjectionGate(t *testing.T) {
	db := newMonoagentTestDB(t)
	mt := NewMonoagentTools(db.DB, "")
	ctx := context.Background()
	raw, err := mt.ExecuteContext(ctx, "register_publication", `{"platform":"blog","kind":"article","title":"Our article"}`)
	if err != nil {
		t.Fatal(err)
	}
	var entry publication.Entry
	if err = json.Unmarshal([]byte(raw), &entry); err != nil {
		t.Fatal(err)
	}
	if _, err = mt.ExecuteContext(ctx, "get_publication", `{"id":"`+entry.ID+`"}`); err != nil {
		t.Fatal(err)
	}
	mt.markSyncedCommsSeen()
	if _, err = mt.ExecuteContext(ctx, "register_publication", `{"platform":"blog","kind":"article","title":"injected"}`); err == nil {
		t.Fatal("registration allowed after untrusted communications")
	}
	if _, err = mt.ExecuteContext(ctx, "list_publications", `{}`); err != nil {
		t.Fatal(err)
	}
}
