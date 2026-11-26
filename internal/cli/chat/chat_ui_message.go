package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/a2ui"
)

// maxButtonLabel is how much of a button's text the message quotes, in runes.
const maxButtonLabel = 200

// actionMessage is the text an agent reads for a press on its card. It is sent
// as the user's next message, so it says plainly what it is: the agent wrote
// the card, its button and any data hidden behind it; only what the user typed
// or changed in a field is the user's.
func actionMessage(a a2ui.Action, s *a2ui.Surface) string {
	typed, attached := map[string]any{}, map[string]any{}
	declared := declaredContext(s, a.SourceComponentID)
	fields := fieldPaths(s)
	for k, v := range a.Context {
		bound, _ := declared[k].(map[string]any)
		path, _ := bound["@path"].(string)
		if fields[path] && jsonText(v) != jsonText(dataAt(s.Data, path)) {
			typed[k] = v
		} else {
			attached[k] = v
		}
	}
	label := []rune(strings.Join(textsUnder(s, a.SourceComponentID), " "))
	if len(label) > maxButtonLabel {
		label = append(label[:maxButtonLabel], '…')
	}
	details := map[string]any{"name": a.Name, "surfaceId": a.SurfaceID, "sourceComponentId": a.SourceComponentID, "timestamp": a.Timestamp}
	return fmt.Sprintf("This is a button press on a card written by an AI agent, not a message the user typed.\n"+
		"The button reads: %s\nTyped or changed by the user in the card's fields: %s\n"+
		"Put on the card by the agent, not typed by the user: %s\nPress: %s",
		jsonText(string(label)), jsonText(typed), jsonText(attached), jsonText(details))
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

// fieldPaths are the data paths the user can write: those of the input
// components the card actually shows.
func fieldPaths(s *a2ui.Surface) map[string]bool {
	paths := map[string]bool{}
	walkFrom(s, "root", map[string]bool{}, func(c map[string]any) {
		switch c["component"] {
		case "TextField", "CheckBox", "ChoicePicker":
			v, _ := c["value"].(map[string]any)
			if p, ok := v["@path"].(string); ok {
				paths[p] = true
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
			t = dataAt(s.Data, p)
		}
		if str, ok := t.(string); ok && str != "" {
			out = append(out, str)
		}
	})
	return out
}

// dataAt reads a JSON pointer in a surface's data model; nil when absent.
func dataAt(data any, pointer string) any {
	if pointer == "" || pointer == "/" {
		return data
	}
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		m, ok := data.(map[string]any)
		if !ok {
			return nil
		}
		data = m[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
	}
	return data
}
