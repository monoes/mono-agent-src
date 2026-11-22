package jev

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Model is one entry of GET /v1/models.
type Model struct {
	ID string `json:"id"`
}

// Models lists the models the key can use (GET /v1/models). It accepts the
// OpenAI-style {"data":[{"id":…}]} list as well as a bare array or a
// {"models":[…]} wrapper, with entries as objects (id or name) or strings.
// A client with a Backend asks the backend (ModelLister).
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	if c.Backend != nil {
		return c.backendModels(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev: request failed: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("jev: read response: %w", err)
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("jev: HTTP %d: %s", res.StatusCode, snippet(raw))
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		var wrapped struct {
			Data   []json.RawMessage `json:"data"`
			Models []json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, fmt.Errorf("jev: undecodable models list: %v", err)
		}
		entries = append(wrapped.Data, wrapped.Models...)
	}
	out := make([]Model, 0, len(entries))
	for _, e := range entries {
		var id string
		if json.Unmarshal(e, &id) != nil {
			var obj struct{ ID, Name string }
			if err := json.Unmarshal(e, &obj); err != nil {
				return nil, fmt.Errorf("jev: undecodable model entry: %v", err)
			}
			id = obj.ID
			if id == "" {
				id = obj.Name
			}
		}
		if id != "" {
			out = append(out, Model{ID: id})
		}
	}
	return out, nil
}
