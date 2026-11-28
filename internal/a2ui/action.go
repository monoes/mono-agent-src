package a2ui

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Action is the renderer-to-agent "action" message: the user pressed a Button.
type Action struct {
	Name              string
	SurfaceID         string
	SourceComponentID string
	Timestamp         string
	Context           map[string]any
	// Revision names the version of the card the user answered, "<turn>:<seq>"
	// of the last journal event that drew it ("" when the renderer says none).
	Revision string
}

// ParseAction decodes a renderer-to-agent action envelope.
func ParseAction(raw []byte, lim Limits) (Action, error) {
	if len(raw) > lim.MaxActionBytes {
		return Action{}, fmt.Errorf("a2ui: action is over the %d byte cap", lim.MaxActionBytes)
	}
	env, err := decodeObject(raw)
	if err != nil {
		return Action{}, fmt.Errorf("a2ui: %w", err)
	}
	if v, _ := env["version"].(string); v != Version || len(env) != 2 {
		return Action{}, errors.New("a2ui: an action envelope is {version, action}")
	}
	return parseActionBody(env["action"], lim)
}

// Press is what the renderer sends back: one action, or, when several cards
// wait for the user, the whole set of them sent together.
type Press struct {
	Set     bool
	Actions []Action
	// Skipped are the cards of a set the user chose to leave unanswered.
	Skipped []string
	// AppendBytes is how much the caller adds to the set's next-turn message (a
	// message's choices and forms).
	AppendBytes int
}

// ParsePress decodes an action envelope {version, action} or a set envelope
// {version, actions, skipped?}.
func ParsePress(raw []byte, lim Limits) (Press, error) {
	if len(raw) > lim.MaxSetBytes {
		return Press{}, fmt.Errorf("a2ui: a set of actions is over the %d byte cap", lim.MaxSetBytes)
	}
	env, err := decodeObject(raw)
	if err != nil {
		return Press{}, fmt.Errorf("a2ui: %w", err)
	}
	list, set := env["actions"]
	if !set {
		a, err := ParseAction(raw, lim)
		if err != nil {
			return Press{}, err
		}
		return Press{Actions: []Action{a}}, nil
	}
	if v, _ := env["version"].(string); v != Version {
		return Press{}, errors.New("a2ui: a set envelope is {version, actions, skipped?}")
	}
	if err := onlyKeys(env, "version", "actions", "skipped", "appendBytes"); err != nil {
		return Press{}, fmt.Errorf("a2ui: %w", err)
	}
	bodies, ok := list.([]any)
	if !ok || len(bodies) == 0 || len(bodies) > lim.MaxSetActions {
		return Press{}, fmt.Errorf("a2ui: actions must list 1 to %d actions", lim.MaxSetActions)
	}
	p := Press{Set: true}
	if v, present := env["appendBytes"]; present {
		n, ok := v.(json.Number)
		i, err := n.Int64()
		if !ok || err != nil || i < 0 || i > int64(lim.MaxSetBytes) {
			return Press{}, errors.New("a2ui: appendBytes must be a byte count")
		}
		p.AppendBytes = int(i)
	}
	for _, b := range bodies {
		if one, err := json.Marshal(b); err != nil || len(one) > lim.MaxActionBytes {
			return Press{}, fmt.Errorf("a2ui: action is over the %d byte cap", lim.MaxActionBytes)
		}
		a, err := parseActionBody(b, lim)
		if err != nil {
			return Press{}, err
		}
		p.Actions = append(p.Actions, a)
	}
	if v, present := env["skipped"]; present {
		ids, ok := v.([]any)
		if !ok || len(ids) > lim.MaxSetActions {
			return Press{}, fmt.Errorf("a2ui: skipped must list at most %d surface ids", lim.MaxSetActions)
		}
		for _, e := range ids {
			id, ok := e.(string)
			if !ok || id == "" {
				return Press{}, errors.New("a2ui: skipped must list surface ids")
			}
			p.Skipped = append(p.Skipped, id)
		}
	}
	return p, nil
}

func parseActionBody(v any, lim Limits) (Action, error) {
	body, ok := v.(map[string]any)
	if !ok {
		return Action{}, errors.New("a2ui: only action messages are accepted from the renderer")
	}
	if err := onlyKeys(body, "name", "surfaceId", "sourceComponentId", "timestamp", "context", "userMessage", "revision"); err != nil {
		return Action{}, fmt.Errorf("a2ui: %w", err)
	}
	a := Action{}
	a.Name, _ = body["name"].(string)
	a.SurfaceID, _ = body["surfaceId"].(string)
	a.SourceComponentID, _ = body["sourceComponentId"].(string)
	a.Timestamp, _ = body["timestamp"].(string)
	a.Context, _ = body["context"].(map[string]any)
	if v, present := body["revision"]; present {
		rev, ok := v.(string)
		if !ok || len(rev) > 200 {
			return Action{}, errors.New("a2ui: revision must be a short string")
		}
		a.Revision = rev
	}
	if a.Name == "" || a.SurfaceID == "" || a.SourceComponentID == "" || a.Context == nil {
		return Action{}, errors.New("a2ui: action needs name, surfaceId, sourceComponentId and context")
	}
	for k, v := range a.Context {
		if err := checkReturnedValue(v, lim); err != nil {
			return Action{}, fmt.Errorf("a2ui: context.%s: %w", k, err)
		}
	}
	return a, nil
}

// CheckAction reports whether the action belongs to a live surface and to a
// Button that surface declared with that exact name. This is the guard that
// stops a forged or stale action reaching an approval path.
func (r *Registry) CheckAction(a Action) (*Surface, error) {
	s := r.Surface(a.SurfaceID)
	if s == nil {
		return nil, fmt.Errorf("a2ui: surface %q is not live", a.SurfaceID)
	}
	if !s.DeclaresAction(a.SourceComponentID, a.Name) {
		return nil, fmt.Errorf("a2ui: surface %q has no button %q that sends %q", a.SurfaceID, a.SourceComponentID, a.Name)
	}
	return s, nil
}
