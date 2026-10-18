package chat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/publication"
)

func publicationToolDefs() []ToolDef {
	makeDef := func(name, desc string, props map[string]interface{}, required ...string) ToolDef {
		schema := map[string]interface{}{"type": "object", "properties": props}
		if len(required) > 0 {
			schema["required"] = required
		}
		return ToolDef{Type: "function", Function: ToolFunction{Name: name, Description: desc, Parameters: schema}}
	}
	return []ToolDef{
		makeDef("list_publications", "List published content in this profile, newest first.", publication.FilterSchema()),
		makeDef("get_publication", "Read a publication's content, destination and publishing source.", map[string]interface{}{"id": strParam("Publication ID")}, "id"),
		makeDef("publication_stats", "Count this profile's publications by platform and kind.", nil),
		makeDef("register_publication", "Record content already successfully published with an external tool. This does not publish. Built-in publishing nodes record automatically. Supply exact content, destination, remote URL/ID and your agent identity; use an idempotency key to avoid duplicate registration.", publication.EntrySchema(), "platform", "kind"),
	}
}

func (mt *MonoagentTools) executePublication(ctx context.Context, name, args string) (string, bool, error) {
	switch name {
	case "list_publications", "get_publication", "publication_stats", "register_publication":
	default:
		return "", false, nil
	}
	store := publication.NewStore(mt.db, mt.ProfileID())
	var result interface{}
	var err error
	switch name {
	case "list_publications":
		var f publication.Filter
		if err = json.Unmarshal([]byte(args), &f); err == nil {
			result, err = store.List(ctx, f)
		}
	case "get_publication":
		var a struct {
			ID string `json:"id"`
		}
		if err = json.Unmarshal([]byte(args), &a); err == nil {
			if a.ID == "" {
				err = fmt.Errorf("id is required")
			} else {
				result, err = store.Get(ctx, a.ID)
			}
		}
	case "publication_stats":
		result, err = store.Stats(ctx)
	case "register_publication":
		if err = mt.checkInjectionGate(name); err != nil {
			break
		}
		var e publication.Entry
		if err = json.Unmarshal([]byte(args), &e); err == nil {
			result, err = store.Register(ctx, e)
		}
	}
	if err != nil {
		return "", true, err
	}
	b, err := json.Marshal(result)
	return string(b), true, err
}
