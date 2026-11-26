package ai

import (
	"fmt"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// The turns that only carry cards are not turns of the chat: the newest turn,
// a page of history and the running turn are read as if they were not there.
func TestCardTurnsAreNotTheChatsTurns(t *testing.T) {
	s, _ := newChatEventStore(t)
	c, err := s.CreateConversation("default", "agent", "general", "claude", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "running"} {
		if _, _, err := s.CreateTurn(c.ID, "default", id, "i", "go "+id); err != nil {
			t.Fatal(err)
		}
		if id == "first" {
			if _, _, err := s.FinalizeTurn("default", c.ID, id, chatevents.StatusFailed, "boom", nil, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 60; i++ {
		if _, err := s.RecordUIAction("default", c.ID, chatevents.UIActionPayload{SurfaceID: "s", Name: "n", Outcome: "rejected"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RecordUISurfaces("default", c.ID, []chatevents.UISurfacePayload{{SurfaceID: fmt.Sprintf("hil-%d", i), Provenance: "system",
			Message: []byte(`{"version":"1.0","createSurface":{"surfaceId":"x"}}`)}}); err != nil {
			t.Fatal(err)
		}
	}
	turns, next, err := s.ListTurns(c.ID, "default", "", 50)
	if err != nil || len(turns) != 2 || next != "" {
		t.Fatalf("%d turns, next %q, %v", len(turns), next, err)
	}
	if latest, _, err := s.ListTurns(c.ID, "default", "", 1); err != nil || len(latest) != 1 || latest[0].IsCard() {
		t.Fatalf("latest %+v %v", latest, err)
	}
	cards, next, err := s.ListCardTurns(c.ID, "default", "", 100)
	if err != nil || len(cards) != 100 || next == "" {
		t.Fatalf("%d cards, next %q, %v", len(cards), next, err)
	}
	rest, next, err := s.ListCardTurns(c.ID, "default", next, 100)
	if err != nil || len(rest) != 20 || next != "" {
		t.Fatalf("%d more, next %q, %v", len(rest), next, err)
	}
	for _, turn := range append(cards, rest...) {
		if !turn.IsCard() {
			t.Fatalf("%q listed as a card turn", turn.ID)
		}
	}
	if active, err := s.ActiveTurn(c.ID, "default"); err != nil || active.ID != "running" {
		t.Fatalf("active %+v %v", active, err)
	}
}
