package chat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// maxButtonLabel is how much of a button's text the message quotes, in runes.
const maxButtonLabel = 200

// actionMessage is the text an agent reads for a press on its card. It is sent
// as the user's next message, so it says plainly what it is: the agent wrote
// the card, its button and any data hidden behind it. A value counts as the
// user's only when it sits in a field the card shows and the card never held it
// (history: the surface after each of its events, so an update racing the press
// is covered). In doubt it is the agent's.
func actionMessage(a a2ui.Action, s *a2ui.Surface, history []*a2ui.Surface) string {
	typed, changed, chosen, attached := map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}
	fields := fieldsOf(s)
	for k, v := range a.Context {
		path := boundPath(s, a.SourceComponentID, k)
		field, shown := fields[path]
		if !shown || !usersOwn(v, path, k, a.SourceComponentID, history) {
			attached[k] = v
			continue
		}
		had, state := resolvePointer(s.Data, path)
		switch {
		case field["component"] == "ChoicePicker":
			chosen[k] = map[string]any{"value": v, "shownAs": optionLabels(s, field, v)}
		case state == pathFound && !isEmptyValue(had):
			changed[k] = map[string]any{"agentHadFilledIn": had, "now": v}
		default:
			typed[k] = v
		}
	}
	label := []rune(strings.Join(textsUnder(s, a.SourceComponentID), " "))
	if len(label) > maxButtonLabel {
		label = append(label[:maxButtonLabel], '…')
	}
	details := map[string]any{"name": a.Name, "surfaceId": a.SurfaceID, "sourceComponentId": a.SourceComponentID, "timestamp": a.Timestamp}
	return fmt.Sprintf("This is a button press on a card written by an AI agent, not a message the user typed.\n"+
		"The button reads: %s\nTyped by the user into an empty field: %s\n"+
		"Changed by the user from what the agent had filled in: %s\nChosen by the user among options the agent wrote: %s\n"+
		"Put on the card by the agent, not typed by the user: %s\nPress: %s",
		jsonText(string(label)), jsonText(typed), jsonText(changed), jsonText(chosen), jsonText(attached), jsonText(details))
}

// usersOwn says the value of context key k is one the user gave: whenever the
// button declared the key it bound it to this same path, the host can follow the path at
// every state of the card, no state held the value there, and it is not just an
// untouched empty field.
func usersOwn(v any, path, k, button string, history []*a2ui.Surface) bool {
	if isEmptyValue(v) || len(history) == 0 {
		return false
	}
	for _, s := range history {
		if _, declared := declaredContext(s, button)[k]; declared && boundPath(s, button, k) != path {
			return false
		}
		had, state := resolvePointer(s.Data, path)
		if state == pathUnknown || (state == pathFound && sameValue(had, v)) {
			return false
		}
	}
	return true
}

// surfaceHistory is a surface after each of its journaled events.
func surfaceHistory(events []chatevents.Event, surfaceID string) []*a2ui.Surface {
	reg := a2ui.NewRegistry(a2ui.DefaultLimits)
	var out []*a2ui.Surface
	for _, ev := range events {
		var p chatevents.UISurfacePayload
		if ev.Type != chatevents.EventUISurface || json.Unmarshal(ev.Payload, &p) != nil || p.SurfaceID != surfaceID {
			continue
		}
		prov := a2ui.FromAgent
		if p.Provenance == string(a2ui.FromSystem) {
			prov = a2ui.FromSystem
		}
		if _, err := reg.HandleFrom(p.Message, prov, dynorg.SurfaceAuthor(ev.TurnID, p.AgentID)); err != nil {
			continue
		}
		if s := reg.Surface(surfaceID); s != nil {
			out = append(out, s)
		}
	}
	return out
}

// boundPath is the data path a button's context key is bound to, "" for a
// literal or none.
func boundPath(s *a2ui.Surface, button, key string) string {
	bound, _ := declaredContext(s, button)[key].(map[string]any)
	path, _ := bound["@path"].(string)
	return path
}

func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case []any:
		return len(t) == 0
	}
	return false
}

// sameValue compares a data model value with a returned one as a renderer would
// show them: numbers by value, and a number or boolean like its text.
func sameValue(a, b any) bool {
	plain := func(v any) any {
		var out any
		_ = json.Unmarshal([]byte(jsonText(v)), &out)
		return out
	}
	pa, pb := plain(a), plain(b)
	if reflect.DeepEqual(pa, pb) {
		return true
	}
	switch pa.(type) {
	case float64, bool:
		return jsonText(pa) == fmt.Sprint(pb)
	}
	return false
}

func optionLabels(s *a2ui.Surface, picker map[string]any, value any) []string {
	picked := map[string]bool{}
	if list, ok := value.([]any); ok {
		for _, v := range list {
			picked[fmt.Sprint(v)] = true
		}
	} else {
		picked[fmt.Sprint(value)] = true
	}
	labels := []string{}
	options, _ := picker["options"].([]any)
	for _, o := range options {
		option, _ := o.(map[string]any)
		if v, _ := option["value"].(string); !picked[v] {
			continue
		}
		label := option["label"]
		if bound, ok := label.(map[string]any); ok {
			p, _ := bound["@path"].(string)
			label, _ = resolvePointer(s.Data, p)
		}
		if str, ok := label.(string); ok {
			labels = append(labels, str)
		}
	}
	return labels
}

// jsonText is v on one line.
func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func declaredContext(s *a2ui.Surface, button string) map[string]any {
	action, _ := s.Components[button]["action"].(map[string]any)
	ev, _ := action["event"].(map[string]any)
	ctx, _ := ev["context"].(map[string]any)
	return ctx
}

// walkFrom visits id and what it holds, each component once.
func walkFrom(s *a2ui.Surface, id string, seen map[string]bool, visit func(map[string]any)) {
	c := s.Components[id]
	if c == nil || seen[id] {
		return
	}
	seen[id] = true
	visit(c)
	if child, ok := c["child"].(string); ok {
		walkFrom(s, child, seen, visit)
	}
	kids, _ := c["children"].([]any)
	for _, k := range kids {
		if kid, ok := k.(string); ok {
			walkFrom(s, kid, seen, visit)
		}
	}
}

// fieldsOf maps the data paths the user can write, those of the input
// components the card actually shows, to the component.
func fieldsOf(s *a2ui.Surface) map[string]map[string]any {
	paths := map[string]map[string]any{}
	walkFrom(s, "root", map[string]bool{}, func(c map[string]any) {
		switch c["component"] {
		case "TextField", "CheckBox", "ChoicePicker":
			v, _ := c["value"].(map[string]any)
			if p, ok := v["@path"].(string); ok {
				paths[p] = c
			}
		}
	})
	return paths
}

// textsUnder are the texts a component shows.
func textsUnder(s *a2ui.Surface, id string) []string {
	var out []string
	walkFrom(s, id, map[string]bool{}, func(c map[string]any) {
		if c["component"] != "Text" {
			return
		}
		t := c["text"]
		if bound, ok := t.(map[string]any); ok {
			p, _ := bound["@path"].(string)
			t, _ = resolvePointer(s.Data, p)
		}
		if str, ok := t.(string); ok && str != "" {
			out = append(out, str)
		}
	})
	return out
}

type pathState int

const (
	pathFound   pathState = iota
	pathAbsent            // a key or index that is not there
	pathUnknown           // the renderers might read something else, or nothing certain
)

// maxPointerDepth is the shallower of the two renderers' limits.
const maxPointerDepth = 24

// resolvePointer reads a data path as both renderers do (a2uiModel.js getPointer,
// a2ui_model.js getAt): through objects and by plain index through arrays. What
// they would not agree on, or could not follow, is pathUnknown.
func resolvePointer(data any, pointer string) (any, pathState) {
	if pointer == "" || pointer == "/" {
		return data, pathFound
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, pathUnknown
	}
	parts := strings.Split(pointer[1:], "/")
	if len(parts) > maxPointerDepth {
		return nil, pathUnknown
	}
	for _, part := range parts {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		if part == "__proto__" || part == "constructor" || part == "prototype" {
			return nil, pathUnknown
		}
		switch node := data.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, pathAbsent
			}
			data = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || strconv.Itoa(i) != part {
				return nil, pathUnknown
			}
			if i >= len(node) {
				return nil, pathAbsent
			}
			data = node[i]
		default:
			return nil, pathUnknown
		}
	}
	return data, pathFound
}
