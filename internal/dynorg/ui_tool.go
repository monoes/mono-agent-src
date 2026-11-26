package dynorg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// The ui_render caller tool: the lead and the workers of a dynamic-org coder
// chat can show the user a small A2UI surface (a form, a choice, a
// confirmation). Every message is validated against the trusted catalog
// (internal/a2ui) before it is journaled as a ui.surface event; the
// button presses come back through `chat ui action`.
//
// PromptSnippet (a2ui.PromptSnippet) is appended to the system prompt at two
// sites, both only when Config.UI is set:
//   - the lead: internal/cli/chat/chat_org.go (next to dynorg.LeadPrompt);
//   - each worker: Conductor.workerTools (ask.go), next to askRule.

// ToolUIRender is the tool's name.
const ToolUIRender = "ui_render"

// MaxUIMessages caps the envelopes of one ui_render call.
const MaxUIMessages = 20

// uiFailureLimit is how many failed ui_render calls in a row (per author) make
// the tool tell the model to answer in markdown.
const uiFailureLimit = 2

// UIConfig turns the ui_render tool on for a conductor. Seed returns the
// conversation's ui.surface events from earlier turns, oldest first, so a
// surface can be updated across turns; nil starts empty.
type UIConfig struct {
	Seed func() ([]chatevents.Event, error)
}

// uiState is the conductor's live registry and failure counts.
type uiState struct {
	mu       sync.Mutex
	reg      *a2ui.Registry
	failures map[string]int // by author: "" lead, else worker id
}

// UIToolSpecs is the ui_render tool; the lead and the workers get the same one.
func UIToolSpecs() []monomind.ToolSpec {
	return []monomind.ToolSpec{{
		Name: ToolUIRender,
		Description: "Show the user a small interface (a form, a choice, a confirmation) when they must fill in or press something. " +
			"Takes A2UI v1.0 messages (createSurface, updateComponents, updateDataModel, deleteSurface). Not for plain answers: those stay markdown. " +
			"A rejected call says why; fix it and resend.",
		Schema: obj(map[string]any{"messages": map[string]any{
			"type": "array", "description": "A2UI v1.0 envelopes, applied in order.",
			"items": map[string]any{"type": "object"},
		}}, "messages"),
	}}
}

// uiRender runs one ui_render call of the lead (w nil) or of a worker.
func (c *Conductor) uiRender(ctx context.Context, w *worker, args json.RawMessage) (string, error) {
	if c.cfg.UI == nil {
		return "", errors.New("ui_render is not available in this chat")
	}
	var a struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(orEmpty(args), &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	author := ""
	if w != nil {
		author = w.id
	}
	st := c.uiStateFor()
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := c.uiSeed(st); err != nil {
		return "", err
	}
	rendered, err := c.applyUI(st, author, a.Messages)
	if err != nil {
		st.failures[author]++
		if st.failures[author] >= uiFailureLimit {
			err = fmt.Errorf("%v. This is the %d. failed ui_render in a row: answer in markdown instead", err, st.failures[author])
		}
		return "", err
	}
	st.failures[author] = 0
	return marshal(map[string]any{"rendered": rendered, "next": "The user sees it now. Their button presses arrive as your next message; end your turn after saying what you wait for."})
}

func (c *Conductor) uiStateFor() *uiState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ui == nil {
		c.ui = &uiState{failures: map[string]int{}}
	}
	return c.ui
}

// uiSeed builds the registry on first use, from the earlier turns' events.
func (c *Conductor) uiSeed(st *uiState) error {
	if st.reg != nil {
		return nil
	}
	var evs []chatevents.Event
	if c.cfg.UI.Seed != nil {
		var err error
		if evs, err = c.cfg.UI.Seed(); err != nil {
			return fmt.Errorf("could not load the conversation's surfaces: %v", err)
		}
	}
	st.reg = RebuildRegistry(evs).Registry
	return nil
}

// applyUI validates and journals the envelopes in order, stops at the first
// invalid one, then checks every surface it touched; a surface that fails the
// check is deleted (the deleteSurface is journaled too) so the user never sees
// a half-built one. It returns the surfaces left live, or the error text the
// model should read.
func (c *Conductor) applyUI(st *uiState, author string, msgs []json.RawMessage) ([]string, error) {
	if len(msgs) == 0 {
		return nil, errors.New("messages is empty")
	}
	if len(msgs) > MaxUIMessages {
		return nil, fmt.Errorf("at most %d messages per call, got %d", MaxUIMessages, len(msgs))
	}
	var touched []string
	seen := map[string]bool{}
	var failure error
	for i, raw := range msgs {
		m, err := st.reg.HandleFrom(raw, a2ui.FromAgent, author)
		if err != nil {
			failure = fmt.Errorf("messages[%d]: %v", i, err)
			break
		}
		c.emitUI(author, m.SurfaceID, raw)
		if !seen[m.SurfaceID] {
			seen[m.SurfaceID] = true
			touched = append(touched, m.SurfaceID)
		}
	}
	var live []string
	for _, id := range touched {
		s := st.reg.Surface(id)
		if s == nil {
			continue
		}
		if err := s.Check(a2ui.DefaultLimits); err != nil {
			c.dropSurface(st, author, id)
			if failure == nil {
				failure = fmt.Errorf("surface %q was removed: %v", id, err)
			}
			continue
		}
		live = append(live, id)
	}
	if failure != nil {
		return nil, failure
	}
	return live, nil
}

// dropSurface deletes a surface that failed its check, in the registry and the journal.
func (c *Conductor) dropSurface(st *uiState, author, id string) {
	raw, _ := json.Marshal(map[string]any{"version": a2ui.Version, string(a2ui.KindDeleteSurface): map[string]any{"surfaceId": id}})
	if _, err := st.reg.HandleFrom(raw, a2ui.FromAgent, author); err == nil {
		c.emitUI(author, id, raw)
	}
}

func (c *Conductor) emitUI(author, surfaceID string, raw json.RawMessage) {
	c.cfg.Emit.Emit(chatevents.EventUISurface, chatevents.UISurfacePayload{
		SurfaceID: surfaceID, Provenance: string(a2ui.FromAgent), AgentID: author, Message: append(json.RawMessage(nil), raw...),
	})
}

// UIReplay is the state of a conversation's surfaces rebuilt from its journal.
type UIReplay struct {
	Registry *a2ui.Registry
	// Authors maps each live surface to the agent that authored it ("" for
	// the lead and for system surfaces).
	Authors map[string]string
	// Closed holds the surfaces the journal created and later deleted, so an
	// action for one of them is stale rather than forged.
	Closed map[string]bool
}

// RebuildRegistry replays a conversation's ui.surface events, in order, into a
// fresh registry. An event that no longer validates is skipped, so its surface
// is simply not live (an action for it is refused): replay fails closed per
// surface, never open. An accepted ui.action on an agent's surface settles it.
// Other event types are ignored.
func RebuildRegistry(events []chatevents.Event) *UIReplay {
	r := &UIReplay{Registry: a2ui.NewRegistry(a2ui.DefaultLimits), Authors: map[string]string{}, Closed: map[string]bool{}}
	for _, ev := range events {
		if ev.Type == chatevents.EventUIAction {
			// An answered agent card frees its slot. A host card is settled by the notice that replaces it.
			var a chatevents.UIActionPayload
			if json.Unmarshal(ev.Payload, &a) == nil && a.Outcome == "accepted" {
				if s := r.Registry.Surface(a.SurfaceID); s != nil && s.Prov == a2ui.FromAgent {
					r.Registry.Settle(a.SurfaceID)
				}
			}
			continue
		}
		if ev.Type != chatevents.EventUISurface {
			continue
		}
		var p chatevents.UISurfacePayload
		if json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		prov := a2ui.FromAgent
		if p.Provenance == string(a2ui.FromSystem) {
			prov = a2ui.FromSystem
		}
		m, err := r.Registry.HandleFrom(p.Message, prov, p.AgentID)
		if err != nil {
			continue
		}
		switch m.Kind {
		case a2ui.KindCreateSurface:
			r.Authors[m.SurfaceID] = p.AgentID
			delete(r.Closed, m.SurfaceID)
		case a2ui.KindDeleteSurface:
			delete(r.Authors, m.SurfaceID)
			r.Closed[m.SurfaceID] = true
		}
	}
	return r
}
