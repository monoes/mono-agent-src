package dynorg

import (
	"encoding/json"
	"errors"
	"strings"
)

// acceptanceText reads an acceptance given as one string or as a list of criteria (joined one per line).
// Models send the list on their first try; refusing it costs a round trip.
func acceptanceText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", errors.New("acceptance must be a string or a list of strings")
	}
	kept := list[:0]
	for _, c := range list {
		if c = strings.TrimSpace(c); c != "" {
			kept = append(kept, c)
		}
	}
	return strings.Join(kept, "\n"), nil
}

// UnmarshalJSON accepts acceptance as a string or a list of strings.
func (p *PlanTask) UnmarshalJSON(b []byte) error {
	type plain PlanTask
	aux := struct {
		*plain
		Acceptance json.RawMessage `json:"acceptance"`
	}{plain: (*plain)(p)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	a, err := acceptanceText(aux.Acceptance)
	if err != nil {
		return err
	}
	p.Acceptance = a
	return nil
}

// UnmarshalJSON accepts acceptance as a string or a list of strings.
func (p *SplitPart) UnmarshalJSON(b []byte) error {
	type plain SplitPart
	aux := struct {
		*plain
		Acceptance json.RawMessage `json:"acceptance"`
	}{plain: (*plain)(p)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	a, err := acceptanceText(aux.Acceptance)
	if err != nil {
		return err
	}
	p.Acceptance = a
	return nil
}
