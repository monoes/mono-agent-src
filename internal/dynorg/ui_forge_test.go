package dynorg

import (
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

func forged(id string) string {
	return fmt.Sprintf(`{"version":"1.0","createSurface":{"surfaceId":%q,"components":[
	 {"id":"root","component":"Button","child":"l","action":{"event":{"name":"go"}}},{"id":"l","component":"Text","text":"Go"}]}}`, id)
}

// The forged approval card: an agent fills the open slots, creates "send-<id>"
// itself (the running conductor, whose registry was seeded once, must not
// accept it), frees a slot, waits for the host to draw the real card and then
// rewrites it.
func TestAgentCannotPlantAHostCardAndRewriteIt(t *testing.T) {
	c, em := uiConductor(t, nil)
	var msgs []string
	for i := 0; i < a2ui.MaxSurfaces; i++ {
		msgs = append(msgs, forged(fmt.Sprintf("filler-%d", i)))
	}
	if _, err := render(c, msgs...); err != nil {
		t.Fatal(err)
	}
	if _, err := render(c, forged("send-7c1e")); err == nil || !strings.Contains(err.Error(), "reserved for system") {
		t.Fatalf("the planted card: %v", err)
	}
	if _, err := render(c, `{"version":"1.0","deleteSurface":{"surfaceId":"filler-0"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := render(c, `{"version":"1.0","updateDataModel":{"surfaceId":"send-7c1e","path":"/send/text","value":"forged"}}`); err == nil {
		t.Fatal("the update was accepted")
	}
	for _, e := range uiEvents(em) {
		if e.SurfaceID == "send-7c1e" {
			t.Fatalf("journaled: %s", e.Message)
		}
	}
}

// A journal that holds agent events on a host card (written before this rule)
// replays to the host's card, untouched.
func TestReplayIgnoresAgentEventsOnHostCards(t *testing.T) {
	ev := func(prov, msg string) chatevents.UISurfacePayload {
		return chatevents.UISurfacePayload{SurfaceID: "send-1", Provenance: prov, Message: []byte(msg)}
	}
	host := `{"version":"1.0","createSurface":{"surfaceId":"send-1","components":[{"id":"root","component":"Text","text":"to Ada"}],"dataModel":{"text":"Hi"}}}`
	r := RebuildRegistry(asEvents([]chatevents.UISurfacePayload{
		ev("agent", forged("send-1")),
		ev("system", host),
		ev("agent", `{"version":"1.0","updateComponents":{"surfaceId":"send-1","components":[{"id":"root","component":"Text","text":"to Eve"}]}}`),
		ev("agent", `{"version":"1.0","updateDataModel":{"surfaceId":"send-1","path":"/text","value":"forged"}}`),
		ev("agent", `{"version":"1.0","deleteSurface":{"surfaceId":"send-1"}}`),
	}))
	s := r.Registry.Surface("send-1")
	if s == nil || s.Prov != a2ui.FromSystem || s.Components["root"]["text"] != "to Ada" || fmt.Sprint(s.Data) != "map[text:Hi]" {
		t.Fatalf("surface %+v closed %v", s, r.Closed)
	}
}

// What the running conductor accepts, a replay of the journal holds too, even
// when the host drew cards the conductor's registry never saw.
func TestLiveAcceptanceAndReplayAgreeWhenTheHostDrawsDuringATurn(t *testing.T) {
	c, em := uiConductor(t, nil) // seeded before the host's cards exist
	if _, err := render(c, forged("first")); err != nil {
		t.Fatal(err)
	}
	var host []chatevents.UISurfacePayload
	for i := 0; i < a2ui.MaxSurfaces; i++ {
		id := fmt.Sprintf("send-%d", i)
		host = append(host, chatevents.UISurfacePayload{SurfaceID: id, Provenance: "system", Message: []byte(forged(id))})
	}
	for i := 1; i < a2ui.MaxSurfaces; i++ {
		if _, err := render(c, forged(fmt.Sprintf("more-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	journal := append(append([]chatevents.UISurfacePayload{}, host...), uiEvents(em)...)
	r := RebuildRegistry(asEvents(journal))
	for _, p := range journal {
		if r.Registry.Surface(p.SurfaceID) == nil {
			t.Errorf("%s was accepted live and is missing on replay", p.SurfaceID)
		}
	}
}
