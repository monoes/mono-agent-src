package jev

import "testing"

func TestGateValue(t *testing.T) {
	choice := Answer{Type: TypeChoice, Choice: "b", Probabilities: map[string]float64{"a": 0.1, "b": 0.9}}
	noul := Answer{Type: TypeNoul, Noul: 0.7}
	for _, c := range []struct {
		name   string
		resp   *Response
		a      Answer
		wantID string
		wantP  float64
	}{
		{"native choice", &Response{Probability: ProbNative}, choice, "b", 0.9},
		{"no kind recorded", &Response{}, choice, "b", 0.9},
		{"nil response", nil, choice, "b", 0.9},
		{"logprob noul", &Response{Probability: ProbLogprob}, noul, "true", 0.7},
		{"sampled", &Response{Probability: ProbSampled}, choice, "b", 0.9},
		{"none choice", &Response{Probability: ProbNone}, choice, "b", 0},
		{"none noul", &Response{Probability: ProbNone}, noul, "true", 0},
	} {
		id, p := GateValue(c.resp, c.a)
		if id != c.wantID || p != c.wantP {
			t.Errorf("%s: GateValue = %q, %v; want %q, %v", c.name, id, p, c.wantID, c.wantP)
		}
		if got := GateProbability(c.resp, 0.5); (c.wantP == 0) != (got == 0) || got != 0 && got != 0.5 {
			t.Errorf("%s: GateProbability = %v", c.name, got)
		}
	}
}

func TestModelLabel(t *testing.T) {
	for _, c := range []struct {
		provider, model, want string
	}{
		{"", "jev-1.13", "jev-1.13"},
		{ProviderTypeSafe, "jev-1.13", "jev-1.13"},
		{"local", "llama3.2:3b", "local/llama3.2:3b"},
		{"openrouter", "openai/gpt-4.1-mini", "openrouter/openai/gpt-4.1-mini"},
		{"hf", "", "hf"},
		{"hf", "  ", "hf"},
		{"local", "my  model\tv2 ", "local/my-model-v2"},
		{ProviderTypeSafe, "", ""},
	} {
		if got := ModelLabel(c.provider, c.model); got != c.want {
			t.Errorf("ModelLabel(%q, %q) = %q, want %q", c.provider, c.model, got, c.want)
		}
	}
	if got := ResponseLabel(&Response{Provider: "local", Model: "m"}, "fallback"); got != "local/m" {
		t.Errorf("ResponseLabel = %q", got)
	}
	if got := ResponseLabel(&Response{Provider: ProviderTypeSafe}, "jev-1.13"); got != "jev-1.13" {
		t.Errorf("ResponseLabel with the client's model = %q", got)
	}
	if got := ResponseLabel(&Response{Provider: "hf"}, ""); got != "hf" {
		t.Errorf("ResponseLabel with no model at all = %q, want no trailing slash", got)
	}
	if got := ResponseLabel(nil, "jev-1.13"); got != "jev-1.13" {
		t.Errorf("ResponseLabel(nil) = %q", got)
	}
}
