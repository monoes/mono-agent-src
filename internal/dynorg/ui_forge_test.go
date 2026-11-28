package dynorg

import (
	"context"
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

// A worker cannot remove or rewrite the lead's card, or another worker's.
func TestAWorkerCannotTouchAnothersCard(t *testing.T) {
	mk := func(agent, msg string) chatevents.UISurfacePayload {
		return chatevents.UISurfacePayload{SurfaceID: "s1", Provenance: "agent", AgentID: agent, Message: []byte(msg)}
	}
	c, em := uiConductor(t, asEvents([]chatevents.UISurfacePayload{mk("w1", forged("s1"))}))
	for _, m := range []string{`{"version":"1.0","deleteSurface":{"surfaceId":"s1"}}`, uiData} {
		if _, err := render(c, m); err == nil { // the lead
			t.Errorf("the lead changed a worker's card: %s", m)
		}
	}
	if len(uiEvents(em)) != 0 {
		t.Fatalf("journaled: %+v", uiEvents(em))
	}
	r := RebuildRegistry(asEvents([]chatevents.UISurfacePayload{
		mk("w1", forged("s1")), mk("w2", `{"version":"1.0","deleteSurface":{"surfaceId":"s1"}}`), mk("", uiData)}))
	if s := r.Registry.Surface("s1"); s == nil || r.Closed["s1"] || fmt.Sprint(s.Data) != "map[]" {
		t.Fatalf("replay applied another author's events: %+v", s)
	}
}

// A later turn's w1 is another agent than an earlier turn's: it can neither
// change nor delete that card, live or on replay. Within one turn it can.
func TestAReusedWorkerIDDoesNotOwnAnEarlierTurnsCard(t *testing.T) {
	in := func(turn string, msg string) chatevents.Event {
		evs := asEvents([]chatevents.UISurfacePayload{{SurfaceID: "s1", Provenance: "agent", AgentID: "w1", Message: []byte(msg)}})
		evs[0].TurnID = turn
		return evs[0]
	}
	del := `{"version":"1.0","deleteSurface":{"surfaceId":"s1"}}`
	seed := []chatevents.Event{in("t1", forged("s1"))}
	for turn, want := range map[string]bool{"t2": false, "t1": true} {
		c, em := uiConductor(t, seed)
		c.cfg.TurnID = turn
		for _, m := range []string{uiData, del} {
			_, err := c.uiRender(context.Background(), &worker{id: "w1"}, uiArgs(m))
			if (err == nil) != want {
				t.Errorf("live, turn %s: %s: %v", turn, m, err)
			}
		}
		if got := len(uiEvents(em)) > 0; got != want {
			t.Errorf("turn %s journaled: %v", turn, got)
		}
		r := RebuildRegistry(append(append([]chatevents.Event{}, seed...), in(turn, del)))
		if gone := r.Registry.Surface("s1") == nil; gone != want {
			t.Errorf("replay, turn %s: deleted %v", turn, gone)
		}
	}
	// what a worker makes in its own turn stays its to change on replay too
	c, em := uiConductor(t, nil)
	c.cfg.TurnID = "t9"
	if _, err := c.uiRender(context.Background(), &worker{id: "w1"}, uiArgs(uiCreate, uiBody, uiData)); err != nil {
		t.Fatal(err)
	}
	var evs []chatevents.Event
	for _, e := range asEvents(uiEvents(em)) {
		e.TurnID = "t9"
		evs = append(evs, e)
	}
	if s := RebuildRegistry(evs).Registry.Surface("s1"); s == nil || fmt.Sprint(s.Data) != "map[n:1]" {
		t.Fatalf("replay of the turn's own events: %+v", s)
	}
}
