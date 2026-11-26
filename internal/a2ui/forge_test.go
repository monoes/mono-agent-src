package a2ui

import (
	"fmt"
	"strings"
	"testing"
)

// The host's card ids belong to the host: an agent message that names one is
// refused whatever it says, so a card an approval hangs on cannot be made,
// changed or removed by an agent.
func TestAgentMayNotNameAHostCardID(t *testing.T) {
	for _, id := range []string{"hil-482", "send-7c1e", "ask-w1.q1", "mission-17", "mission-report-17"} {
		msgs := []string{
			card(id, true),
			fmt.Sprintf(`{"version":"1.0","updateComponents":{"surfaceId":%q,"components":[{"id":"root","component":"Text","text":"x"}]}}`, id),
			fmt.Sprintf(`{"version":"1.0","updateDataModel":{"surfaceId":%q,"path":"/send/text","value":"forged"}}`, id),
			fmt.Sprintf(`{"version":"1.0","deleteSurface":{"surfaceId":%q}}`, id),
		}
		// With no such card, and with the host's card live.
		for _, live := range []bool{false, true} {
			r := NewRegistry(DefaultLimits)
			if live {
				mustHandle(t, r, card(id, true), FromSystem)
			}
			for _, m := range msgs {
				if _, err := r.Handle([]byte(m), FromAgent); err == nil || !strings.Contains(err.Error(), "reserved for system") {
					t.Errorf("live=%v: agent message accepted or refused for another reason (%v): %s", live, err, m)
				}
			}
			if live {
				if s := r.Surface(id); s == nil || s.Components["root"]["component"] != "Button" {
					t.Errorf("%s: the host card changed: %+v", id, s)
				}
			}
		}
	}
}

// Agent and host cards have caps of their own: neither can take the other's
// slots, and a flood of agent panels does not push a host card out.
func TestAgentCardsCannotCrowdOutHostCards(t *testing.T) {
	r := NewRegistry(DefaultLimits)
	for i := 0; i < MaxSurfaces; i++ {
		mustHandle(t, r, card(fmt.Sprintf("a-%d", i), true), FromAgent)
	}
	for i := 0; i < MaxSurfaces; i++ {
		mustHandle(t, r, card(fmt.Sprintf("send-%d", i), true), FromSystem)
	}
	for _, c := range []struct {
		id   string
		prov Provenance
	}{{"a-more", FromAgent}, {"send-more", FromSystem}} {
		if _, err := r.Handle([]byte(card(c.id, true)), c.prov); err == nil {
			t.Errorf("%s: past the %s cap", c.id, c.prov)
		}
	}

	r = NewRegistry(DefaultLimits)
	mustHandle(t, r, card("send-note", false), FromSystem)
	for i := 0; i < MaxKeptSurfaces+20; i++ {
		mustHandle(t, r, card(fmt.Sprintf("panel-%d", i), false), FromAgent)
	}
	if r.Surface("send-note") == nil {
		t.Fatal("agent panels pushed a host card out of the registry")
	}
	if r.Surface("panel-0") != nil {
		t.Fatal("agent panels are not bounded")
	}
}
