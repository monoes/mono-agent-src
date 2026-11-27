package surfaces

import (
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// Every button that decides an item carries the digest of the item as its card
// shows it, and Route hands it to the host apart from the edited fields.
func TestDecisionButtonsCarryTheCardsDigest(t *testing.T) {
	it := sampleHIL()
	it.Digest = "d-hil"
	hil, err := HIL(it)
	if err != nil {
		t.Fatal(err)
	}
	p := samplePayload()
	p.Attachments = nil
	send, err := Send(p, "d-send")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		msgs                      [][]byte
		surface, kind, id, digest string
	}{{hil, "hil-482", RefHIL, "482", "d-hil"}, {send, "send-7c1e", RefSend, "7c1e", "d-send"}} {
		s := surfaceOf(t, c.msgs, c.surface)
		for button, name := range map[string]string{btnApprove: "hil.approve", btnReject: "hil.reject"} {
			ctx := s.Components[button]["action"].(map[string]any)["event"].(map[string]any)["context"].(map[string]any)
			if ctx[DigestKey] != c.digest {
				t.Fatalf("%s %s: context %v", c.surface, button, ctx)
			}
			cmd, err := Route(act(name, c.surface, map[string]any{"id": c.id, DigestKey: c.digest}), chatevents.UIRef{Kind: c.kind, ID: c.id})
			if err != nil || cmd.Digest != c.digest || len(cmd.Fields) != 0 {
				t.Fatalf("%s: %+v %v", name, cmd, err)
			}
		}
	}
	if _, err := Route(act("mission.stop", "mission-17", map[string]any{"sessionId": "17", DigestKey: "x"}), chatevents.UIRef{Kind: RefMissionPlan, ID: "17"}); err == nil {
		t.Fatal("a digest on a mission button")
	}
	it.EditableData = map[string]any{DigestKey: "forged"}
	if _, err := HIL(it); err == nil || errors.Is(err, ErrTooLarge) {
		t.Fatalf("an editable field took the digest's name: %v", err)
	}
}
