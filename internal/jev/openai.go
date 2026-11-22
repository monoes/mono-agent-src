package jev

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

// ProtocolOpenAI is OpenAI's Decisions API (POST /v1/decisions). Its questions
// are an ordered array and its answers come back in the same order; the
// adapter below maps that to and from the systemone shape the rest of the
// code uses: choice → choice, score → score, noul → predicate.

func jsonText(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// encodeOpenAI builds the /v1/decisions body and returns the question ids in
// the order sent.
func encodeOpenAI(req Request) (body []byte, order []string, err error) {
	var state any
	raw, err := json.Marshal(req.State)
	if err != nil {
		return nil, nil, err
	}
	_ = json.Unmarshal(raw, &state)
	if _, ok := state.(map[string]any); !ok {
		state = map[string]any{"state": state}
	}
	order = make([]string, 0, len(req.Questions))
	for id := range req.Questions {
		order = append(order, id)
	}
	sort.Strings(order)
	qs := make([]map[string]any, 0, len(order))
	for _, id := range order {
		q := req.Questions[id]
		out := map[string]any{"name": id}
		ins := jsonText(q.Instructions)
		switch q.Type {
		case TypeNoul:
			out["type"] = "predicate"
			if m, ok := q.Criteria.(map[string]any); ok {
				ins += fmt.Sprintf(" True means: %s. False means: %s.", jsonText(m["true"]), jsonText(m["false"]))
			}
		case TypeChoice:
			ids := OptionIDs(q)
			sort.Strings(ids)
			choices := make([]map[string]any, 0, len(ids))
			for _, o := range ids {
				c := map[string]any{"value": o}
				if d := jsonText(criteriaAt(q.Criteria, o)); d != "" {
					c["description"] = d
				}
				choices = append(choices, c)
			}
			out["type"], out["choices"] = "choice", choices
		case TypeScore:
			ids := OptionIDs(q)
			levels := make([]map[string]any, len(ids))
			for i := range ids {
				levels[i] = map[string]any{"label": jsonText(criteriaAt(q.Criteria, strconv.Itoa(i)))}
			}
			out["type"], out["levels"] = "score", levels
		default:
			return nil, nil, fmt.Errorf("unknown question type %q", q.Type)
		}
		if ins == "" {
			ins = id
		}
		out["instructions"] = ins
		qs = append(qs, out)
	}
	body, err = json.Marshal(map[string]any{
		"model":     req.Model,
		"input":     []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_json", "data": state}}}},
		"questions": qs,
	})
	return body, order, err
}

// criteriaAt is the description of option id in a choice map or the level
// label at index id of a score list.
func criteriaAt(c any, id string) any {
	switch v := c.(type) {
	case map[string]any:
		return v[id]
	case map[string]string:
		return v[id]
	case []string:
		if i, err := strconv.Atoi(id); err == nil && i >= 0 && i < len(v) {
			return v[i]
		}
	case []any:
		if i, err := strconv.Atoi(id); err == nil && i >= 0 && i < len(v) {
			return v[i]
		}
	}
	return nil
}

type openAIAnswer struct {
	Type          string  `json:"type"`
	Probability   float64 `json:"probability"`
	Choice        any     `json:"choice"`
	Score         float64 `json:"score"`
	Confidence    float64 `json:"confidence"`
	Probabilities []struct {
		Probability float64 `json:"probability"`
		Value       any     `json:"value"`
		Label       string  `json:"label"`
	} `json:"probabilities"`
}

// decodeOpenAI maps a /v1/decisions reply back to a systemone Response keyed by
// the question ids in order.
func decodeOpenAI(raw []byte, order []string, questions map[string]Question) (Response, error) {
	var in struct {
		Model   string         `json:"model"`
		Answers []openAIAnswer `json:"answers"`
		Usage   Usage          `json:"usage"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return Response{}, fmt.Errorf("%w: undecodable response: %v", ErrInvalidAnswer, err)
	}
	if len(in.Answers) != len(order) {
		return Response{}, fmt.Errorf("%w: %d answers for %d questions", ErrInvalidAnswer, len(in.Answers), len(order))
	}
	out := Response{Model: in.Model, Usage: in.Usage, Answers: map[string]Answer{}}
	for i, id := range order {
		a, q := in.Answers[i], questions[id]
		switch a.Type {
		case "refusal":
			return Response{}, fmt.Errorf("%w: the provider refused %q", ErrInvalidAnswer, id)
		case "predicate":
			out.Answers[id] = Answer{Type: TypeNoul, Noul: a.Probability}
		case "choice":
			probs := map[string]float64{}
			for _, p := range a.Probabilities {
				probs[fmt.Sprint(p.Value)] = p.Probability
			}
			out.Answers[id] = Answer{Type: TypeChoice, Choice: fmt.Sprint(a.Choice), Probabilities: probs, Confidence: a.Confidence}
		case "score":
			probs := map[string]float64{}
			for _, p := range a.Probabilities {
				for _, lid := range OptionIDs(q) {
					if fmt.Sprint(criteriaAt(q.Criteria, lid)) == p.Label {
						probs[lid] = p.Probability
					}
				}
			}
			out.Answers[id] = Answer{Type: TypeScore, Score: a.Score, Probabilities: probs, Confidence: a.Confidence}
		default:
			return Response{}, fmt.Errorf("%w: answer type %q for %q", ErrInvalidAnswer, a.Type, id)
		}
	}
	return out, nil
}
