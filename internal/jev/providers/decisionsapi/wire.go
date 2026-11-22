package decisionsapi

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/monoes/mono-agent/internal/jev"
)

// criteriaAt is the description of option id in a choice map, or the level
// text at index id of a score list.
func criteriaAt(c any, id string) string {
	switch v := c.(type) {
	case map[string]any:
		return describe(v[id])
	case map[string]string:
		return v[id]
	case []string:
		if i, err := strconv.Atoi(id); err == nil && i >= 0 && i < len(v) {
			return v[i]
		}
	case []any:
		if i, err := strconv.Atoi(id); err == nil && i >= 0 && i < len(v) {
			return describe(v[i])
		}
	}
	return ""
}

func sortedIDs(qs map[string]jev.Question) []string {
	ids := make([]string, 0, len(qs))
	for id := range qs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func stateJSON(state any) any {
	raw, _ := json.Marshal(state)
	var v any
	_ = json.Unmarshal(raw, &v)
	if _, ok := v.(map[string]any); !ok {
		v = map[string]any{"state": v}
	}
	return v
}

// ---- OpenAI: POST /v1/decisions, questions as an ordered array ----

func encodeOpenAI(model string, req *jev.Request) ([]byte, []string, error) {
	order := sortedIDs(req.Questions)
	qs := make([]map[string]any, 0, len(order))
	for _, id := range order {
		q := req.Questions[id]
		out := map[string]any{"name": id}
		ins := describe(q.Instructions)
		switch q.Type {
		case jev.TypeNoul:
			out["type"] = "predicate"
			if m, ok := q.Criteria.(map[string]any); ok {
				ins += fmt.Sprintf(" True means: %s. False means: %s.", describe(m["true"]), describe(m["false"]))
			}
		case jev.TypeChoice:
			ids := jev.OrderedOptionIDs(q)
			choices := make([]map[string]any, 0, len(ids))
			for _, o := range ids {
				c := map[string]any{"value": o}
				if d := criteriaAt(q.Criteria, o); d != "" {
					c["description"] = d
				}
				choices = append(choices, c)
			}
			out["type"], out["choices"] = "choice", choices
		case jev.TypeScore:
			ids := jev.OrderedOptionIDs(q)
			levels := make([]map[string]any, len(ids))
			for i, o := range ids {
				label := criteriaAt(q.Criteria, o)
				if label == "" {
					label = "level " + o
				}
				levels[i] = map[string]any{"label": label}
			}
			out["type"], out["levels"] = "score", levels
		default:
			return nil, nil, fmt.Errorf("%w: question type %q", jev.ErrUnsupported, q.Type)
		}
		if ins == "" {
			ins = id
		}
		out["instructions"] = ins
		qs = append(qs, out)
	}
	body, err := json.Marshal(map[string]any{
		"model":     model,
		"input":     []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_json", "data": stateJSON(req.State)}}}},
		"questions": qs,
	})
	return body, order, err
}

type openAIAnswer struct {
	Type          string  `json:"type"`
	Probability   float64 `json:"probability"`
	Probabilities []struct {
		Probability float64 `json:"probability"`
		Value       any     `json:"value"`
		Label       string  `json:"label"`
	} `json:"probabilities"`
}

func decodeOpenAI(raw []byte, order []string, qs map[string]jev.Question) (*jev.Response, error) {
	var in struct {
		Model   string         `json:"model"`
		Answers []openAIAnswer `json:"answers"`
		Usage   struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	resp := &jev.Response{Answers: map[string]jev.Answer{}}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("%w: openai-decisions: undecodable response", jev.ErrInvalidAnswer)
	}
	resp.Model, resp.Usage = in.Model, jev.Usage{InputTokens: in.Usage.InputTokens, OutputTokens: in.Usage.OutputTokens}
	if len(in.Answers) != len(order) {
		return resp, fmt.Errorf("%w: openai-decisions: %d answers for %d questions", jev.ErrInvalidAnswer, len(in.Answers), len(order))
	}
	for i, id := range order {
		a, q := in.Answers[i], qs[id]
		var probs map[string]float64
		switch {
		case a.Type == "refusal":
			return resp, fmt.Errorf("%w: openai-decisions: the provider refused %q", jev.ErrInvalidAnswer, id)
		case a.Type == "predicate" && q.Type == jev.TypeNoul:
			probs = map[string]float64{"true": a.Probability, "false": 1 - a.Probability}
		case a.Type == "choice" && q.Type == jev.TypeChoice:
			probs = map[string]float64{}
			for _, p := range a.Probabilities {
				probs[fmt.Sprint(p.Value)] = p.Probability
			}
		case a.Type == "score" && q.Type == jev.TypeScore:
			probs = map[string]float64{}
			for _, o := range jev.OrderedOptionIDs(q) {
				for _, p := range a.Probabilities {
					if p.Label == levelLabel(q, o) {
						probs[o] = p.Probability
					}
				}
			}
		default:
			return resp, fmt.Errorf("%w: openai-decisions: answer type %q for a %s question", jev.ErrInvalidAnswer, a.Type, q.Type)
		}
		ans, err := jev.AnswerFrom(q, probs)
		if err != nil {
			return resp, fmt.Errorf("%s: %w", id, err)
		}
		resp.Answers[id] = ans
	}
	return resp, nil
}

func levelLabel(q jev.Question, id string) string {
	if l := criteriaAt(q.Criteria, id); l != "" {
		return l
	}
	return "level " + id
}

// ---- Gemini: POST /v1beta/decisions, systemone-shaped, keys restricted ----

var geminiKeyBad = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// geminiKeys maps each question id to a key Gemini accepts
// ([A-Za-z0-9_-]+, at most 64 characters): ids that already qualify are kept,
// the rest are cleaned, cut and made unique.
func geminiKeys(qs map[string]jev.Question) map[string]string {
	ids := sortedIDs(qs)
	keys, taken := map[string]string{}, map[string]bool{}
	for _, id := range ids {
		if id != "" && len(id) <= 64 && !geminiKeyBad.MatchString(id) {
			keys[id], taken[id] = id, true
		}
	}
	for _, id := range ids {
		if _, ok := keys[id]; ok {
			continue
		}
		base := geminiKeyBad.ReplaceAllString(id, "_")
		if base == "" {
			base = "q"
		}
		if len(base) > 56 {
			base = base[:56]
		}
		k := base
		for n := 2; taken[k]; n++ {
			k = fmt.Sprintf("%s_%d", base, n)
		}
		keys[id], taken[k] = k, true
	}
	return keys
}

// encodeGemini sends each question under its geminiKeys key; decodeGemini
// maps the answers back, so there is no order to return.
func encodeGemini(model string, req *jev.Request) ([]byte, []string, error) {
	state, err := json.Marshal(req.State)
	if err != nil {
		return nil, nil, err
	}
	keys := geminiKeys(req.Questions)
	qs := map[string]jev.Question{}
	for id, q := range req.Questions {
		qs[keys[id]] = q
	}
	body, err := json.Marshal(map[string]any{"model": model, "input": string(state), "questions": qs})
	return body, nil, err
}

func decodeGemini(raw []byte, qs map[string]jev.Question) (*jev.Response, error) {
	var in struct {
		Answers map[string]struct {
			Type          string             `json:"type"`
			Noul          *float64           `json:"noul"`
			Choice        string             `json:"choice"`
			Probabilities map[string]float64 `json:"probabilities"`
		} `json:"answers"`
		Usage struct {
			In  int `json:"total_input_tokens"`
			Out int `json:"total_output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("%w: gemini-decisions: undecodable response", jev.ErrInvalidAnswer)
	}
	resp := &jev.Response{Answers: map[string]jev.Answer{}, Usage: jev.Usage{InputTokens: in.Usage.In, OutputTokens: in.Usage.Out}}
	keys := geminiKeys(qs)
	for id, q := range qs {
		a, ok := in.Answers[keys[id]]
		if !ok {
			return resp, fmt.Errorf("%w: gemini-decisions: no answer for %q", jev.ErrInvalidAnswer, id)
		}
		var probs map[string]float64
		if q.Type == jev.TypeNoul {
			if a.Noul == nil {
				return resp, fmt.Errorf("%w: gemini-decisions: no noul for %q", jev.ErrInvalidAnswer, id)
			}
			probs = map[string]float64{"true": *a.Noul, "false": 1 - *a.Noul}
		} else {
			probs = a.Probabilities
		}
		ans, err := jev.AnswerFrom(q, probs)
		if err != nil {
			return resp, fmt.Errorf("%s: %w", id, err)
		}
		resp.Answers[id] = ans
	}
	return resp, nil
}
