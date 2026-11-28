package a2ui

import (
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
	body, ok := env["action"].(map[string]any)
	if !ok {
		return Action{}, errors.New("a2ui: only action messages are accepted from the renderer")
	}
	if err := onlyKeys(body, "name", "surfaceId", "sourceComponentId", "timestamp", "context", "userMessage"); err != nil {
		return Action{}, fmt.Errorf("a2ui: %w", err)
	}
	a := Action{}
	a.Name, _ = body["name"].(string)
	a.SurfaceID, _ = body["surfaceId"].(string)
	a.SourceComponentID, _ = body["sourceComponentId"].(string)
	a.Timestamp, _ = body["timestamp"].(string)
	a.Context, _ = body["context"].(map[string]any)
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
