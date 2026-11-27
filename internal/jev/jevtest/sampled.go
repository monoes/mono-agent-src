package jevtest

import (
	"fmt"

	"github.com/monoes/mono-agent/internal/jev"
)

// samples is how many answers the adapters take of one question.
const samples = 5

// Sampled is q's answer as a sampling adapter builds it: agree of five samples
// name pick ("" = the first option; "true" or "false" for a noul) and the rest
// another option.
func Sampled(q jev.Question, pick string, agree int) (jev.Answer, error) {
	keys := jev.OrderedOptionIDs(q)
	if q.Type == jev.TypeNoul {
		keys = []string{"true", "false"}
	}
	if len(keys) < 2 {
		return jev.Answer{}, fmt.Errorf("jevtest: a sampled answer needs two options, got %d", len(keys))
	}
	if pick == "" {
		pick = keys[0]
	}
	other := keys[0]
	if other == pick {
		other = keys[1]
	}
	a, err := jev.AnswerFrom(q, jev.SampledProbs(map[string]int{pick: agree, other: samples - agree}, samples, keys))
	a.Unanimous = jev.UnanimousProbability(samples, len(keys))
	return a, err
}

// SetSampled makes f answer as a sampling provider: every question as Sampled,
// picking what h says for it (nil = the first option).
func (f *FakeBackend) SetSampled(h Handler, agree int) {
	f.Caps.Probability = jev.ProbSampled
	f.SetRespond(func(req *jev.Request) (*jev.Response, error) {
		var want map[string]string
		if h != nil {
			want = h(*req)
		}
		resp := &jev.Response{Model: f.Model, Answers: map[string]jev.Answer{}, Probability: jev.ProbSampled}
		for id, q := range req.Questions {
			a, err := Sampled(q, want[id], agree)
			if err != nil {
				return resp, fmt.Errorf("jevtest: question %q: %w", id, err)
			}
			resp.Answers[id] = a
		}
		return resp, nil
	})
}
