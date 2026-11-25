package a2ui

import (
	"fmt"
	"strings"
	"testing"
)

func card(id string, button bool) string {
	root := `{"id":"root","component":"Text","text":"done"}`
	if button {
		root = `{"id":"root","component":"Button","child":"l","action":{"event":{"name":"go"}}},{"id":"l","component":"Text","text":"Go"}`
	}
	return fmt.Sprintf(`{"version":"1.0","createSurface":{"surfaceId":%q,"components":[%s]}}`, id, root)
}

func mustHandle(t *testing.T, r *Registry, raw string, prov Provenance) {
	t.Helper()
	if _, err := r.Handle([]byte(raw), prov); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
}

// A card nobody can act on any more (a decided notice, an answered question) must not hold a slot, or a long
// conversation stops showing cards after its first MaxSurfaces.
func TestOnlyCardsThatCanStillBeActedOnHoldASlot(t *testing.T) {
	r := NewRegistry(DefaultLimits)
	for i := 0; i < 3*MaxSurfaces; i++ { // decided notices
		mustHandle(t, r, card(fmt.Sprintf("notice-%d", i), false), FromSystem)
	}
	for i := 0; i < 3*MaxSurfaces; i++ { // answered questions
		id := fmt.Sprintf("q-%d", i)
		mustHandle(t, r, card(id, true), FromAgent)
		r.Settle(id)
	}
	for i := 0; i < MaxSurfaces; i++ {
		mustHandle(t, r, card(fmt.Sprintf("open-%d", i), true), FromAgent)
	}
	if _, err := r.Handle([]byte(card("one-too-many", true)), FromAgent); err == nil || !strings.Contains(err.Error(), "live surfaces") {
		t.Fatalf("the cap on open cards is gone: %v", err)
	}
	// A card without a button cannot be given one past the cap.
	add := `{"version":"1.0","updateComponents":{"surfaceId":"notice-23","components":[{"id":"root","component":"Button","child":"l","action":{"event":{"name":"go"}}},{"id":"l","component":"Text","text":"Go"}]}}`
	if _, err := r.Handle([]byte(add), FromSystem); err == nil {
		t.Fatal("an update opened a ninth card")
	}
	if s := r.Surface("notice-23"); s == nil || s.Components["root"]["component"] != "Text" {
		t.Fatalf("the refused update changed the card: %+v", s)
	}
}

// What the registry keeps stays bounded: the oldest cards that cannot be acted on are forgotten, never an open one.
func TestKeptSurfacesAreBounded(t *testing.T) {
	r := NewRegistry(DefaultLimits)
	mustHandle(t, r, card("open", true), FromAgent)
	for i := 0; i < MaxKeptSurfaces+50; i++ {
		mustHandle(t, r, card(fmt.Sprintf("n-%d", i), false), FromSystem)
	}
	if got := len(r.surf); got > MaxKeptSurfaces {
		t.Fatalf("registry holds %d surfaces", got)
	}
	if r.Surface("open") == nil || r.Surface("n-0") != nil || r.Surface(fmt.Sprintf("n-%d", MaxKeptSurfaces+49)) == nil {
		t.Fatal("evicted the wrong surface")
	}
}
