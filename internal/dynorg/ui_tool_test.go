package dynorg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

const uiCreate = `{"version":"1.0","createSurface":{"surfaceId":"s1"}}`
const uiBody = `{"version":"1.0","updateComponents":{"surfaceId":"s1","components":[
 {"id":"root","component":"Column","children":["t","b"]},
 {"id":"t","component":"Text","text":"Pick one"},
 {"id":"bl","component":"Text","text":"Go"},
 {"id":"b","component":"Button","child":"bl","action":{"event":{"name":"go","context":{}}}}]}}`
const uiNoRoot = `{"version":"1.0","updateComponents":{"surfaceId":"s1","components":[{"id":"t","component":"Text","text":"x"}]}}`
const uiBadComponent = `{"version":"1.0","updateComponents":{"surfaceId":"s1","components":[{"id":"root","component":"Script","text":"x"}]}}`
const uiData = `{"version":"1.0","updateDataModel":{"surfaceId":"s1","path":"/n","value":1}}`

func uiArgs(msgs ...string) json.RawMessage {
	parts := make([]json.RawMessage, len(msgs))
	for i, m := range msgs {
		parts[i] = json.RawMessage(m)
	}
	b, _ := json.Marshal(map[string]any{"messages": parts})
	return b
}

func uiConductor(t *testing.T, seed []chatevents.Event) (*Conductor, *recEmitter) {
	t.Helper()
	em := &recEmitter{}
	c := New(context.Background(), Config{Cwd: "/w", Staffer: &Staffer{Roster: []Model{askModel}, Lead: askModel}, Exec: (&askingExec{}).exec, Emit: em,
		UI: &UIConfig{Seed: func() ([]chatevents.Event, error) { return seed, nil }}, Limits: Limits{MaxAgents: 4, MaxConcurrent: 4}})
	t.Cleanup(c.Close)
	return c, em
}

func uiEvents(em *recEmitter) []chatevents.UISurfacePayload {
	em.mu.Lock()
	defer em.mu.Unlock()
	var out []chatevents.UISurfacePayload
	for _, e := range em.events {
		if e.typ == chatevents.EventUISurface {
			out = append(out, e.payload.(chatevents.UISurfacePayload))
		}
	}
	return out
}

func asEvents(ps []chatevents.UISurfacePayload) []chatevents.Event {
	var out []chatevents.Event
	for _, p := range ps {
		b, _ := json.Marshal(p)
		out = append(out, chatevents.Event{Type: chatevents.EventUISurface, Payload: b})
	}
	return out
}

func render(c *Conductor, msgs ...string) (string, error) {
	return c.Handle(context.Background(), ToolUIRender, uiArgs(msgs...))
}

func TestUIRenderEmitsEachMessageOfTheLead(t *testing.T) {
	c, em := uiConductor(t, nil)
	out, err := render(c, uiCreate, uiBody)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"s1"`) {
		t.Errorf("result %s", out)
	}
	evs := uiEvents(em)
	if len(evs) != 2 || evs[0].SurfaceID != "s1" || evs[0].Provenance != "agent" || evs[0].AgentID != "" {
		t.Fatalf("events %+v", evs)
	}
	if !strings.Contains(string(evs[1].Message), `"updateComponents"`) {
		t.Errorf("message %s", evs[1].Message)
	}
}

func TestUIRenderInvalidMessageStopsTheBatchAndSaysWhy(t *testing.T) {
	c, em := uiConductor(t, nil)
	_, err := render(c, uiCreate, uiBadComponent, uiBody)
	if err == nil || !strings.Contains(err.Error(), "messages[1]") {
		t.Fatalf("err = %v", err)
	}
	// The create went out, the bad one and what followed did not; the half-built surface is removed.
	evs := uiEvents(em)
	if len(evs) != 2 || !strings.Contains(string(evs[1].Message), `"deleteSurface"`) {
		t.Fatalf("events %+v", evs)
	}
}

func TestUIRenderRemovesASurfaceThatFailsItsCheck(t *testing.T) {
	c, em := uiConductor(t, nil)
	_, err := render(c, uiCreate, uiNoRoot)
	if err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("err = %v", err)
	}
	evs := uiEvents(em)
	if len(evs) != 3 || !strings.Contains(string(evs[2].Message), `"deleteSurface"`) {
		t.Fatalf("events %+v", evs)
	}
	if _, err := render(c, uiData); err == nil || !strings.Contains(err.Error(), "not created") {
		t.Errorf("the removed surface is still live: %v", err)
	}
}

// The second failed call in a row tells the model to answer in markdown; a call that works starts the count over.
func TestUIRenderFailuresInARowSayToUseMarkdown(t *testing.T) {
	c, _ := uiConductor(t, nil)
	_, err1 := render(c, uiBadComponent)
	_, err2 := render(c, uiBadComponent)
	if err1 == nil || strings.Contains(err1.Error(), "markdown") {
		t.Fatalf("first failure: %v", err1)
	}
	if err2 == nil || !strings.Contains(err2.Error(), "answer in markdown") {
		t.Fatalf("second failure: %v", err2)
	}
	if _, err := render(c, uiCreate, uiBody); err != nil {
		t.Fatal(err)
	}
	if _, err := render(c, uiBadComponent); err == nil || strings.Contains(err.Error(), "markdown") {
		t.Errorf("the count did not restart after a success: %v", err)
	}
}

func TestUIRenderCapsTheMessagesOfOneCall(t *testing.T) {
	c, em := uiConductor(t, nil)
	msgs := []string{uiCreate, uiBody}
	for len(msgs) <= MaxUIMessages {
		msgs = append(msgs, uiData)
	}
	_, err := render(c, msgs...)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at most %d messages", MaxUIMessages)) {
		t.Fatalf("err = %v", err)
	}
	if n := len(uiEvents(em)); n != 0 {
		t.Errorf("%d messages were applied before the cap refused the call", n)
	}
	if _, err := render(c, msgs[:MaxUIMessages]...); err != nil {
		t.Errorf("a call of exactly %d messages: %v", MaxUIMessages, err)
	}
	if _, err := render(c); err == nil {
		t.Error("an empty call was accepted")
	}
}

func TestUIRenderReachesSurfacesOfEarlierTurns(t *testing.T) {
	c1, em1 := uiConductor(t, nil)
	if _, err := render(c1, uiCreate, uiBody); err != nil {
		t.Fatal(err)
	}
	c2, _ := uiConductor(t, asEvents(uiEvents(em1)))
	if _, err := render(c2, uiData); err != nil {
		t.Errorf("update across turns: %v", err)
	}
	c3, _ := uiConductor(t, nil)
	if _, err := render(c3, uiData); err == nil {
		t.Error("an unseeded conductor updated a surface it never saw")
	}
	cSeedErr := New(context.Background(), Config{Emit: &recEmitter{}, UI: &UIConfig{Seed: func() ([]chatevents.Event, error) { return nil, errors.New("db gone") }}})
	defer cSeedErr.Close()
	if _, err := render(cSeedErr, uiCreate); err == nil || !strings.Contains(err.Error(), "db gone") {
		t.Errorf("seed failure: %v", err)
	}
}

func TestUIRenderIsOffWithoutConfig(t *testing.T) {
	c := New(context.Background(), Config{Emit: &recEmitter{}})
	defer c.Close()
	if _, err := render(c, uiCreate); err == nil {
		t.Error("ui_render ran without Config.UI")
	}
}

func TestUIRenderRefusesTheReservedActionNames(t *testing.T) {
	c, _ := uiConductor(t, nil)
	reserved := strings.Replace(uiBody, `"name":"go"`, `"name":"hil.approve"`, 1)
	if _, err := render(c, uiCreate, reserved); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("agent surface with hil.approve: %v", err)
	}
}

// A worker gets the tool and the prompt, and its surfaces carry its id.
func TestUIRenderOfAWorker(t *testing.T) {
	em := &recEmitter{}
	var got monomind.ExecOptions
	var result string
	var rerr error
	exec := func(ctx context.Context, o monomind.ExecOptions, on func(monomind.Event)) (*monomind.TurnResult, error) {
		got = o
		on(monomind.Event{Type: monomind.EventStart})
		result, rerr = o.OnToolCall(ctx, ToolUIRender, uiArgs(uiCreate, uiBody))
		return okTurn("done"), nil
	}
	c := New(context.Background(), Config{Cwd: "/w", Staffer: &Staffer{Roster: []Model{askModel}, Lead: askModel}, Exec: exec, Emit: em,
		UI: &UIConfig{}, Limits: Limits{MaxAgents: 4, MaxConcurrent: 4}})
	defer c.Close()
	info, _ := c.Spawn(context.Background(), SpawnRequest{Brief: "make a form", Wait: true})
	if rerr != nil {
		t.Fatal(rerr)
	}
	var names []string
	for _, tl := range got.Tools {
		names = append(names, tl.Name)
	}
	if len(names) != 1 || names[0] != ToolUIRender || !strings.Contains(got.SystemPrompt, a2ui.PromptSnippet()) {
		t.Errorf("tools %v, snippet present %v", names, strings.Contains(got.SystemPrompt, "ui_render"))
	}
	evs := uiEvents(em)
	if len(evs) != 2 || evs[0].AgentID != info.ID || info.ID == "" {
		t.Errorf("events %+v for worker %q (result %s)", evs, info.ID, result)
	}
}

func TestRebuildRegistryReplaysInOrder(t *testing.T) {
	mk := func(agent, prov, msg string) chatevents.UISurfacePayload {
		return chatevents.UISurfacePayload{SurfaceID: "s1", Provenance: prov, AgentID: agent, Message: json.RawMessage(msg)}
	}
	create2 := strings.Replace(uiCreate, "s1", "s2", 1)
	evs := asEvents([]chatevents.UISurfacePayload{
		mk("w1", "agent", uiCreate), mk("w1", "agent", uiBody), mk("w1", "agent", uiData),
		{SurfaceID: "s2", Provenance: "agent", Message: json.RawMessage(create2)},
		mk("w1", "agent", `{"version":"1.0","deleteSurface":{"surfaceId":"s1"}}`),
		mk("w2", "agent", uiCreate), mk("w2", "agent", uiBody),
		mk("w2", "agent", uiBadComponent), // no longer valid: skipped, the surface keeps its last good state
	})
	evs = append(evs, chatevents.Event{Type: chatevents.EventUIAction, Payload: []byte(`{}`)}, chatevents.Event{Type: chatevents.EventUISurface, Payload: []byte(`not json`)})
	r := RebuildRegistry(evs)
	s := r.Registry.Surface("s1")
	if s == nil || s.Check(a2ui.DefaultLimits) != nil || r.Authors["s1"] != "w2" || r.Closed["s1"] {
		t.Fatalf("s1 = %+v authors %v closed %v", s, r.Authors, r.Closed)
	}
	if r.Registry.Surface("s2") == nil || r.Authors["s2"] != "" {
		t.Errorf("s2 authors %v", r.Authors)
	}
	if err := func() error {
		_, err := r.Registry.CheckAction(a2ui.Action{Name: "go", SurfaceID: "s1", SourceComponentID: "b"})
		return err
	}(); err != nil {
		t.Errorf("the rebuilt surface does not declare its button: %v", err)
	}
}

func TestRebuildRegistryMarksDeletedSurfacesClosed(t *testing.T) {
	r := RebuildRegistry(asEvents([]chatevents.UISurfacePayload{
		{SurfaceID: "s1", Provenance: "agent", Message: json.RawMessage(uiCreate)},
		{SurfaceID: "s1", Provenance: "agent", Message: json.RawMessage(`{"version":"1.0","deleteSurface":{"surfaceId":"s1"}}`)},
	}))
	if r.Registry.Surface("s1") != nil || !r.Closed["s1"] || r.Closed["nope"] {
		t.Errorf("closed %v", r.Closed)
	}
}

// A surface the journal says is the system's keeps that provenance on replay, so an agent cannot take it over.
func TestRebuildRegistryKeepsSystemProvenance(t *testing.T) {
	sys := strings.Replace(uiBody, `"name":"go"`, `"name":"hil.approve"`, 1)
	r := RebuildRegistry(asEvents([]chatevents.UISurfacePayload{
		{SurfaceID: "s1", Provenance: "system", Message: json.RawMessage(uiCreate)},
		{SurfaceID: "s1", Provenance: "system", Message: json.RawMessage(sys)},
		{SurfaceID: "s1", Provenance: "agent", Message: json.RawMessage(uiData)},
	}))
	s := r.Registry.Surface("s1")
	if s == nil || s.Prov != a2ui.FromSystem || !s.DeclaresAction("b", "hil.approve") {
		t.Fatalf("surface %+v", s)
	}
	if _, ok := s.Data.(map[string]any)["n"]; ok {
		t.Error("an agent message updated a system surface on replay")
	}
}

func TestRebuildRegistryFreesTheSlotOfAnAnsweredAgentCard(t *testing.T) {
	var evs []chatevents.Event
	add := func(typ chatevents.EventType, payload any) {
		ev, err := chatevents.New("p", "c", "t", int64(len(evs)+1), time.Unix(0, 0), typ, payload)
		if err != nil {
			t.Fatal(err)
		}
		evs = append(evs, ev)
	}
	for i := 0; i < a2ui.MaxSurfaces+3; i++ {
		id := fmt.Sprintf("q-%d", i)
		msg := fmt.Sprintf(`{"version":"1.0","createSurface":{"surfaceId":%q,"components":[{"id":"root","component":"Button","child":"l","action":{"event":{"name":"go"}}},{"id":"l","component":"Text","text":"Go"}]}}`, id)
		add(chatevents.EventUISurface, chatevents.UISurfacePayload{SurfaceID: id, Provenance: "agent", Message: json.RawMessage(msg)})
		add(chatevents.EventUIAction, chatevents.UIActionPayload{SurfaceID: id, Name: "go", SourceComponentID: "root", Outcome: "accepted"})
	}
	r := RebuildRegistry(evs)
	last := fmt.Sprintf("q-%d", a2ui.MaxSurfaces+2)
	if r.Registry.Surface(last) == nil {
		t.Fatal("answered cards kept their slots: a later card was dropped")
	}
}
