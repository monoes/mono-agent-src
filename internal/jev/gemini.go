package jev

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

// geminiKeyBad matches what Gemini does not allow in a question key
// ([A-Za-z0-9_-]+, at most 64 characters).
var geminiKeyBad = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// geminiKeys maps each question id to a key Gemini accepts. Ids that already
// qualify are kept; the rest are cleaned, cut to 64 characters and made unique.
func geminiKeys(questions map[string]Question) map[string]string {
	ids := make([]string, 0, len(questions))
	for id := range questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	keys, taken := map[string]string{}, map[string]bool{}
	for _, id := range ids {
		if !geminiKeyBad.MatchString(id) && id != "" && len(id) <= 64 {
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

// ProtocolGemini is Google's Decisions API. Its questions and answers have the
// systemone shape (a keyed map of noul/choice/score); only the path, the key
// header, the `input` field and the usage names differ.

// encodeGemini returns the body and the key each question was sent under.
func encodeGemini(req Request) ([]byte, map[string]string, error) {
	state, err := json.Marshal(req.State)
	if err != nil {
		return nil, nil, err
	}
	keys := geminiKeys(req.Questions)
	qs := make(map[string]Question, len(req.Questions))
	for id, q := range req.Questions {
		qs[keys[id]] = q
	}
	body, err := json.Marshal(map[string]any{"model": req.Model, "input": string(state), "questions": qs})
	return body, keys, err
}

// geminiRekey puts the answers back under the caller's question ids.
func geminiRekey(r *Response, keys map[string]string) {
	out := make(map[string]Answer, len(r.Answers))
	for id, k := range keys {
		if a, ok := r.Answers[k]; ok {
			out[id] = a
		}
	}
	r.Answers = out
}

func geminiUsage(raw []byte) Usage {
	var u struct {
		Usage struct {
			In  int `json:"total_input_tokens"`
			Out int `json:"total_output_tokens"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(raw, &u)
	return Usage{InputTokens: u.Usage.In, OutputTokens: u.Usage.Out}
}
