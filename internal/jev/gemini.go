package jev

import "encoding/json"

// ProtocolGemini is Google's Decisions API. Its questions and answers have the
// systemone shape (a keyed map of noul/choice/score); only the path, the key
// header, the `input` field and the usage names differ.

func encodeGemini(req Request) ([]byte, error) {
	state, err := json.Marshal(req.State)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"model": req.Model, "input": string(state), "questions": req.Questions})
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
