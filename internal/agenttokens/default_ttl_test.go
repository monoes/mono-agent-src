package agenttokens

import (
	"context"
	"testing"
	"time"
)

func TestAgentTokenDefaultsTo90DaysAndNoExpiryIsExplicit(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	def, _, err := s.Create(ctx, "default", "dflt", ScopeRead, 0)
	if err != nil {
		t.Fatal(err)
	}
	if def.ExpiresAt == nil || def.ExpiresAt.Sub(def.CreatedAt) < 89*24*time.Hour || def.ExpiresAt.Sub(def.CreatedAt) > 91*24*time.Hour {
		t.Fatalf("default token expiry: %v", def.ExpiresAt)
	}
	forever, _, err := s.Create(ctx, "default", "forever", ScopeRead, NoExpiry)
	if err != nil {
		t.Fatal(err)
	}
	if forever.ExpiresAt != nil {
		t.Fatalf("NoExpiry token expires at %v", forever.ExpiresAt)
	}
}
