package jevtest

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
)

func TestFakeBackendAnswersThroughAClient(t *testing.T) {
	fb := NewFakeBackend(Fixed(map[string]string{"route": "billing", "ok": "0.9", "fit": "2"}))
	c := jev.NewBackendClient("fake", "m", fb)
	resp, err := c.Ask(context.Background(), map[string]any{"x": 1}, map[string]jev.Question{
		"route": {Type: jev.TypeChoice, Criteria: map[string]any{"billing": "b", "tech": "t"}},
		"ok":    {Type: jev.TypeNoul},
		"fit":   {Type: jev.TypeScore, Criteria: []string{"no", "some", "yes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answers["route"].Choice != "billing" || resp.Answers["ok"].Noul != 0.9 || resp.Answers["fit"].Score != 2 {
		t.Fatalf("answers = %+v", resp.Answers)
	}
	if resp.Provider != "fake" || resp.Probability != jev.ProbLogprob || fb.Calls() != 1 || fb.Requests()[0].Model != "m" {
		t.Fatalf("resp = %+v, calls = %d", resp, fb.Calls())
	}
}

// The fake does not echo the request's model: it names Model, or none, and
// then the client's model is the label.
func TestFakeBackendNamesItsOwnModel(t *testing.T) {
	fb := NewFakeBackend(nil)
	q := map[string]jev.Question{"q": {Type: jev.TypeNoul}}
	resp, err := fb.Answer(context.Background(), &jev.Request{Model: "asked", Questions: q})
	if err != nil || resp.Model != "" {
		t.Fatalf("model %q %v", resp.Model, err)
	}
	fb.Model = "served"
	if resp, err = jev.NewBackendClient("fake", "asked", fb).Ask(context.Background(), nil, q); err != nil || resp.Model != "served" ||
		jev.ResponseLabel(resp, "asked") != "fake/served" {
		t.Fatalf("model %q %v", resp.Model, err)
	}
	fb.Model = ""
	if resp, err = jev.NewBackendClient("fake", "", fb).Ask(context.Background(), nil, q); err != nil || jev.ResponseLabel(resp, "") != "fake" {
		t.Fatalf("label %q %v", jev.ResponseLabel(resp, ""), err)
	}
}

func TestFakeBackendCanFailAndBreakAnswers(t *testing.T) {
	fb := NewFakeBackend(nil)
	c := jev.NewBackendClient("fake", "", fb)
	q := map[string]jev.Question{"q": {Type: jev.TypeChoice, Criteria: map[string]any{"a": "A"}}}
	boom := errors.New("boom")
	fb.SetErr(boom)
	if _, err := c.Ask(context.Background(), nil, q); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	fb.SetErr(nil)
	fb.SetRespond(func(*jev.Request) (*jev.Response, error) {
		return &jev.Response{Answers: map[string]jev.Answer{"q": {Type: jev.TypeChoice, Choice: "zzz", Probabilities: map[string]float64{"zzz": 1}}}}, nil
	})
	if _, err := c.Ask(context.Background(), nil, q); !errors.Is(err, jev.ErrInvalidAnswer) {
		t.Fatalf("a broken answer passed: %v", err)
	}
	fb.SetRespond(nil)
	if _, err := c.Ask(context.Background(), nil, q); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Models(context.Background()); !errors.Is(err, jev.ErrUnsupported) {
		t.Fatalf("models: %v", err)
	}
	fb.ModelList = []jev.Model{{ID: "m"}}
	if ms, err := c.Models(context.Background()); err != nil || len(ms) != 1 {
		t.Fatalf("models: %v %v", ms, err)
	}
}
