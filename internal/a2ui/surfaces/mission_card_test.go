package surfaces

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// Approving a mission also starts its lead and must name the plan revision on
// screen, which a card cannot do: the plan card offers no approval and says
// where it is given.
func TestThePlanCardOffersNoApproval(t *testing.T) {
	msgs, err := MissionPlan(samplePlan(), 10)
	if err != nil {
		t.Fatal(err)
	}
	s := surfaceOf(t, msgs, "mission-17")
	for id := range s.Components {
		if s.DeclaresAction(id, "mission.approve") {
			t.Fatalf("%s approves", id)
		}
	}
	if !strings.Contains(string(msgs[0]), "approve and start this plan in the Tasks tab") {
		t.Fatalf("the card does not say where to approve:\n%s", msgs[0])
	}
	_, err = Route(act("mission.approve", "mission-17", map[string]any{"sessionId": "17"}), chatevents.UIRef{Kind: RefMissionPlan, ID: "17"})
	if err == nil || !strings.Contains(err.Error(), "not a host action") {
		t.Fatalf("an old card's Approve is still routed: %v", err)
	}
}

func TestMissionProgressKeepsTheEstimate(t *testing.T) {
	upd, err := MissionProgressUpdate(17, 1, 3, 1, 10, 4.5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(upd[0]), `"estimated":4.5`) {
		t.Fatalf("%s", upd[0])
	}
}
