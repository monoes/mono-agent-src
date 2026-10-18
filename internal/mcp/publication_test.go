package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/publication"
)

func TestPublicationMCPMutationsGated(t *testing.T) {
	for _, allow := range []bool{false, true} {
		names := map[string]bool{}
		for _, def := range toolDefinitions(allow) {
			names[def["name"].(string)] = true
		}
		for _, name := range []string{"publication_list", "publication_get", "publication_stats"} {
			if !names[name] {
				t.Fatalf("missing %s", name)
			}
		}
		if names["publication_register"] != allow {
			t.Fatal("registration gate")
		}
	}
	s := newTestServer(t)
	_, err := callTool(context.Background(), s, "publication_register", json.RawMessage(`{"platform":"custom","kind":"post","body":"hello"}`))
	if err == nil || !strings.Contains(err.Error(), "allow-mutations") {
		t.Fatalf("gate: %v", err)
	}
}
func TestPublicationMCPProfileScope(t *testing.T) {
	s := newTestServerAllowMutations(t, true)
	rt, err := s.runtime()
	if err != nil {
		t.Fatal(err)
	}
	defer s.closeRuntime()
	ctx := context.Background()
	other, err := publication.NewStore(rt.db.DB, "other").Register(ctx, publication.Entry{Platform: "custom", Kind: "post", Body: "private to other profile"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = callTool(ctx, s, "publication_get", json.RawMessage(`{"id":"`+other.ID+`"}`)); err == nil {
		t.Fatal("cross-profile get succeeded")
	}
	raw, err := callTool(ctx, s, "publication_register", json.RawMessage(`{"platform":"custom","kind":"post","body":"ours","profile_id":"other"}`))
	if err != nil {
		t.Fatal(err)
	}
	var e publication.Entry
	if err = json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	if e.ProfileID != rt.profileID {
		t.Fatalf("profile overridden: %+v", e)
	}
	raw, err = callTool(ctx, s, "publication_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var entries []publication.Entry
	if err = json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Body != "ours" {
		t.Fatalf("list: %s", raw)
	}
	raw, err = callTool(ctx, s, "publication_stats", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(raw, `"total": 1`) {
		t.Fatalf("stats: %s %v", raw, err)
	}
}
