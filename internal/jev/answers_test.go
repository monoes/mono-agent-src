package jev

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// Every answer the helpers build passes Validate against its question.
func TestAnswerFromBuildsValidAnswers(t *testing.T) {
	score := Question{Type: TypeScore, Criteria: []string{"poor", "fair", "good", "great"}}
	scoreAny := Question{Type: TypeScore, Criteria: []any{"no", "yes"}}
	noul := Question{Type: TypeNoul}
	for _, c := range []struct {
		name  string
		q     Question
		probs map[string]float64
		check func(t *testing.T, a Answer)
	}{
		{"choice", choiceQ("a", "b", "c"), map[string]float64{"a": 1, "b": 3}, func(t *testing.T, a Answer) {
			if a.Choice != "b" || !near(a.Probabilities["b"], 0.75) || a.Probabilities["c"] != 0 || len(a.Probabilities) != 3 {
				t.Errorf("answer = %+v", a)
			}
			if !near(a.Confidence, ChoiceConfidence(a.Probabilities)) {
				t.Errorf("confidence = %v", a.Confidence)
			}
		}},
		{"choice tie goes to the lowest id", choiceQ("x", "b", "m"), map[string]float64{"x": 1, "m": 1}, func(t *testing.T, a Answer) {
			if a.Choice != "m" {
				t.Errorf("choice = %q", a.Choice)
			}
		}},
		{"choice of one option", choiceQ("only"), map[string]float64{"only": 0.2}, func(t *testing.T, a Answer) {
			if a.Choice != "only" || a.Confidence != 1 {
				t.Errorf("answer = %+v", a)
			}
		}},
		{"score", score, map[string]float64{"2": 0.5, "3": 0.5}, func(t *testing.T, a Answer) {
			if !near(a.Score, 2.5) || len(a.Probabilities) != 4 || a.Legend["0"] != "poor" || a.Legend["3"] != "great" {
				t.Errorf("answer = %+v", a)
			}
		}},
		{"score over []any", scoreAny, map[string]float64{"1": 1}, func(t *testing.T, a Answer) {
			if a.Score != 1 || a.Confidence != 1 || a.Legend["1"] != "yes" {
				t.Errorf("answer = %+v", a)
			}
		}},
		{"noul", noul, map[string]float64{"true": 3, "false": 1}, func(t *testing.T, a Answer) {
			if !near(a.Noul, 0.75) {
				t.Errorf("answer = %+v", a)
			}
		}},
		{"noul of no", noul, map[string]float64{"false": 1}, func(t *testing.T, a Answer) {
			if a.Noul != 0 {
				t.Errorf("answer = %+v", a)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, err := AnswerFrom(c.q, c.probs)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(c.q, a); err != nil {
				t.Fatalf("Validate: %v (%+v)", err, a)
			}
			c.check(t, a)
		})
	}
}

func TestAnswerFromRefusesToGuess(t *testing.T) {
	for _, c := range []struct {
		name  string
		q     Question
		probs map[string]float64
	}{
		{"an option not offered", choiceQ("a"), map[string]float64{"a": 0.5, "z": 0.5}},
		{"no mass", choiceQ("a", "b"), map[string]float64{}},
		{"a negative value", choiceQ("a", "b"), map[string]float64{"a": -1, "b": 2}},
		{"no options", Question{Type: TypeChoice}, map[string]float64{"a": 1}},
		{"a score level out of range", Question{Type: TypeScore, Criteria: []string{"lo", "hi"}}, map[string]float64{"2": 1}},
		{"noul mass on neither", Question{Type: TypeNoul}, map[string]float64{}},
		{"an unknown type", Question{Type: "rank"}, map[string]float64{"a": 1}},
	} {
		if a, err := AnswerFrom(c.q, c.probs); !errors.Is(err, ErrInvalidAnswer) {
			t.Errorf("%s: %+v %v", c.name, a, err)
		}
	}
	if _, err := NoulAnswer(1.2); !errors.Is(err, ErrInvalidAnswer) {
		t.Errorf("noul 1.2: %v", err)
	}
}

func TestOrderedOptionIDs(t *testing.T) {
	if got := OrderedOptionIDs(choiceQ("c", "a", "b")); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("choice: %v", got)
	}
	levels := make([]string, 12)
	got := OrderedOptionIDs(Question{Type: TypeScore, Criteria: levels})
	if len(got) != 12 || got[2] != "2" || got[10] != "10" {
		t.Errorf("score: %v", got)
	}
	if got := OrderedOptionIDs(Question{Type: TypeNoul, Criteria: map[string]any{"true": "y", "false": "n"}}); got != nil {
		t.Errorf("noul: %v", got)
	}
}

func cost(f float64) *float64 { return &f }

func TestFanOutAnswersEveryQuestionWithinTheLimit(t *testing.T) {
	req := &Request{Model: "m", Questions: map[string]Question{}}
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		req.Questions[id] = Question{Type: TypeNoul}
	}
	var inFlight, peak atomic.Int32
	resp, err := FanOut(context.Background(), req, 2, func(_ context.Context, id string, q Question) (Answer, Usage, error) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		a, _ := NoulAnswer(0.5)
		u := Usage{InputTokens: 10, OutputTokens: 1}
		if id == "a" {
			u.CostUSD = cost(0.25)
		}
		return a, u, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if peak.Load() > 2 {
		t.Errorf("%d calls ran at once, want at most 2", peak.Load())
	}
	if len(resp.Answers) != 6 || resp.Model != "m" || resp.Usage.InputTokens != 60 || resp.Usage.OutputTokens != 6 ||
		resp.Usage.CostUSD == nil || *resp.Usage.CostUSD != 0.25 {
		t.Fatalf("resp = %+v", resp)
	}
}

// The first failure cancels the rest and comes back with the usage so far.
func TestFanOutStopsAtTheFirstFailure(t *testing.T) {
	req := &Request{Questions: map[string]Question{"a": {Type: TypeNoul}, "b": {Type: TypeNoul}, "c": {Type: TypeNoul}}}
	boom := errors.New("boom")
	resp, err := FanOut(context.Background(), req, 1, func(ctx context.Context, id string, q Question) (Answer, Usage, error) {
		if id == "b" {
			return Answer{}, Usage{InputTokens: 4}, boom
		}
		if ctx.Err() != nil {
			t.Errorf("%s ran after the failure", id)
		}
		a, _ := NoulAnswer(1)
		return a, Usage{InputTokens: 4}, nil
	})
	if !errors.Is(err, boom) || resp == nil || resp.Usage.InputTokens != 8 || len(resp.Answers) != 1 {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FanOut(ctx, req, 0, func(context.Context, string, Question) (Answer, Usage, error) {
		return Answer{}, Usage{}, nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled context: %v", err)
	}
}
