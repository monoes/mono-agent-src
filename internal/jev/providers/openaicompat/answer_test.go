package openaicompat

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/testdb"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// D4: linking the adapter registers its kind and changes nothing else: with
// no provider configured every surface still uses TypeSafe.
func TestTheKindIsRegisteredAndTheDefaultStaysTypeSafe(t *testing.T) {
	if !slices.Contains(jevconf.Kinds(), Kind) {
		t.Fatalf("kinds = %v", jevconf.Kinds())
	}
	db := testdb.Open(t).DB
	for _, s := range append(jevconf.Surfaces, jevconf.Decider, jevconf.CLI) {
		if p := jevconf.ActiveProvider(db, "default", s); p != jev.ProviderTypeSafe {
			t.Errorf("%s: %q", s, p)
		}
	}
}

// Every question kind, end to end through jev.Client.Ask on a provider of
// kind openai: one request each, one token, log-probabilities read back.
func TestEachQuestionKindEndToEnd(t *testing.T) {
	for _, c := range []struct {
		name, target string
		q            jev.Question
		check        func(a jev.Answer) bool
		labels       []string
	}{
		{"choice", "sales", jev.Question{Type: jev.TypeChoice, Instructions: "Route the ticket.",
			Criteria: map[string]any{"billing": "Money questions", "sales": "Wants to buy", "support": "Needs help"}},
			func(a jev.Answer) bool { return a.Choice == "sales" && near(a.Probabilities["sales"], 0.9) },
			[]string{"A", "B", "C"}},
		{"noul", "No", jev.Question{Type: jev.TypeNoul, Instructions: "Is it spam?",
			Criteria: map[string]any{"true": "spam", "false": "a real ticket"}},
			func(a jev.Answer) bool { return near(a.Noul, 0.1) }, []string{"Yes", "No"}},
		{"score", "good", jev.Question{Type: jev.TypeScore, Instructions: "How polite?", Criteria: []string{"bad", "ok", "good"}},
			func(a jev.Answer) bool { return near(a.Score, 0.9*2+0.05) && near(a.Probabilities["2"], 0.9) },
			[]string{"0", "1", "2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, oracle(t, c.target, 0))
			cl, db := newClient(t, f.URL, "", jev.Capabilities{})
			resp, err := ask(t, cl, c.q)
			if err != nil {
				t.Fatal(err)
			}
			if a := resp.Answers["q"]; !c.check(a) {
				t.Fatalf("answer = %+v", a)
			}
			if resp.Provider != "local" || resp.Probability != jev.ProbLogprob || resp.Model != testModel || resp.Usage.InputTokens != 10 ||
				resp.Usage.OutputTokens != 1 || resp.Usage.CostUSD != nil {
				t.Fatalf("response = %+v", resp)
			}
			reqs := f.requests()
			if len(reqs) != 1 {
				t.Fatalf("%d requests", len(reqs))
			}
			r := reqs[0]
			if r.Method != "POST" || r.Path != "/chat/completions" || r.Body.Model != testModel || r.Body.MaxTokens != 1 ||
				r.Raw["temperature"] != 0.0 || r.Raw["logprobs"] != true || r.Body.TopLogprobs != 20 {
				t.Fatalf("request = %+v %v", r.Body, r.Raw)
			}
			for _, unwanted := range []string{"logit_bias", "response_format", "grammar"} {
				if _, ok := r.Raw[unwanted]; ok {
					t.Errorf("the request sets %s", unwanted)
				}
			}
			if len(r.Body.Messages) != 2 || r.Body.Messages[0].Role != "system" || !strings.Contains(r.Body.Messages[0].Content, "untrusted_") {
				t.Fatalf("messages = %+v", r.Body.Messages)
			}
			user := r.user()
			if !strings.HasPrefix(user, "State (JSON):\n{\"ticket\":\"refund please\"}") || strings.Index(user, "Question:") < 0 {
				t.Fatalf("the state is not first: %q", user)
			}
			if labels, _ := labelsOf(user); !slices.Equal(labels, c.labels) {
				t.Fatalf("labels = %v, want %v", labels, c.labels)
			}
			var prov, kind string
			if err := db.QueryRow(`SELECT provider, probability_kind FROM jev_usage`).Scan(&prov, &kind); err != nil || prov != "local" || kind != "logprob" {
				t.Fatalf("usage row: %q %q %v", prov, kind, err)
			}
		})
	}
}

// The questions of one request are sent one per request, all beginning with
// the same system message and state, so a server can cache that prefix.
func TestTheQuestionsOfARequestShareTheStatePrefix(t *testing.T) {
	f := newFake(t, oracle(t, "A", 0))
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	qs := map[string]jev.Question{"one": choiceOf("x", "y"), "two": choiceOf("p", "q", "r"), "three": choiceOf("m", "n")}
	if _, err := cl.Ask(context.Background(), map[string]any{"b": 1, "a": []int{1, 2}}, qs); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	prefix := "State (JSON):\n{\"a\":[1,2],\"b\":1}\n\nQuestion:"
	for _, r := range reqs {
		if !strings.HasPrefix(r.user(), prefix) || r.Body.Messages[0].Content != systemPrompt {
			t.Errorf("request = %q", r.user())
		}
	}
}

// " A", "a" and "A" are one label; their masses are summed. The answer
// keeps its raw mass (0.8): the 0.05 on "Hello" goes to the other option.
func TestLabelVariantsAreSummed(t *testing.T) {
	f := newFake(t, func(int, seen) (int, any) {
		return 200, logprobReply(alt{"A", 0.5}, alt{" A", 0.2}, alt{"a", 0.1}, alt{"B", 0.15}, alt{"Hello", 0.05})
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf("first", "second"))
	if err != nil {
		t.Fatal(err)
	}
	if a := resp.Answers["q"]; a.Choice != "first" || !near(a.Probabilities["first"], 0.8) || !near(a.Probabilities["second"], 0.2) {
		t.Fatalf("answer = %+v", a)
	}
}

// When most of the first token's mass is on something that is not a label,
// the model wanted to say something else: an invalid answer that quotes none
// of what it said.
func TestLowCoverageIsRejected(t *testing.T) {
	f := newFake(t, func(int, seen) (int, any) {
		return 200, logprobReply(alt{"Certainly", 0.7}, alt{"A", 0.2}, alt{"B", 0.1})
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	_, err := ask(t, cl, choiceOf("first", "second"))
	if !errors.Is(err, jev.ErrInvalidAnswer) || strings.Contains(err.Error(), "Certainly") {
		t.Fatalf("err = %v", err)
	}
}

// A server that returns no log-probabilities is sampled: five answers at
// temperature 1, still one token, smoothed over the samples asked (3 × B,
// 1 × A, 1 miss: B = (3+1)/(5+2)). The backend remembers and asks no more
// for them.
func TestWithoutLogprobsTheAnswerIsSampled(t *testing.T) {
	// Request 0 asks for logprobs; the samples follow, five per question.
	script := func(answers ...string) func(int, seen) (int, any) {
		return func(n int, r seen) (int, any) {
			if r.Body.Logprobs {
				return 200, textReply("B")
			}
			return 200, textReply(answers[(n-1)%len(answers)])
		}
	}
	f := newFake(t, script("B", "B", " A", "B", "Well"))
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	for round := 0; round < 2; round++ {
		before := len(f.requests())
		resp, err := ask(t, cl, choiceOf("first", "second"))
		if err != nil {
			t.Fatal(err)
		}
		if a := resp.Answers["q"]; a.Choice != "second" || !near(a.Probabilities["second"], 4.0/7) || resp.Probability != jev.ProbSampled {
			t.Fatalf("round %d: %+v %s", round, a, resp.Probability)
		}
		reqs := f.requests()[before:]
		want := 6 // one asking for logprobs, five samples
		if round == 1 {
			want = 5
		}
		if len(reqs) != want {
			t.Fatalf("round %d: %d requests", round, len(reqs))
		}
		for _, r := range reqs[want-5:] {
			if _, ok := r.Raw["logprobs"]; ok || r.Raw["temperature"] != 1.0 || r.Body.MaxTokens != 1 {
				t.Fatalf("a sample request: %v", r.Raw)
			}
		}
		if resp.Usage.InputTokens != 10*want {
			t.Fatalf("usage = %+v", resp.Usage)
		}
	}
	if p := cl.Backend.Capabilities().Probability; p != jev.ProbSampled {
		t.Fatalf("capabilities say %s", p)
	}

	// Fewer than three valid samples: invalid, quoting none of them.
	f.setReply(script("Hmm", "Sure", "A", "Okay", "B"))
	_, err := ask(t, cl, choiceOf("first", "second"))
	if !errors.Is(err, jev.ErrInvalidAnswer) || strings.Contains(err.Error(), "Sure") {
		t.Fatalf("err = %v", err)
	}
}

// A stored probe result of sampled (or none) skips the logprobs attempt.
func TestStoredCapabilitiesSkipLogprobs(t *testing.T) {
	for _, p := range []jev.ProbabilityKind{jev.ProbSampled, jev.ProbNone} {
		f := newFake(t, func(int, seen) (int, any) { return 200, textReply("A") })
		cl, _ := newClient(t, f.URL, "", jev.Capabilities{Probability: p})
		resp, err := ask(t, cl, choiceOf("first", "second"))
		if err != nil || resp.Probability != jev.ProbSampled || len(f.requests()) != 5 {
			t.Fatalf("%s: %+v %v (%d requests)", p, resp, err, len(f.requests()))
		}
		if c := cl.Backend.Capabilities(); c.Probability != jev.ProbSampled || c.QuestionsPerRequest != 1 || c.MaxChoiceOptions != 0 || c.OptionsPerRequest != 15 || !c.Models {
			t.Fatalf("%s: caps = %+v", p, c)
		}
	}
}

// A 400 to a request with logprobs (Groq) is retried without them, sampled,
// and remembered. A 400 that is not about logprobs fails and is not.
func TestA400ToLogprobsRetriesWithoutThem(t *testing.T) {
	f := newFake(t, func(_ int, r seen) (int, any) {
		if _, ok := r.Raw["logprobs"]; ok {
			return 400, `{"error":{"message":"logprobs is not supported with this model"}}`
		}
		return 200, textReply("B")
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf("first", "second"))
	if err != nil || resp.Probability != jev.ProbSampled || resp.Answers["q"].Choice != "second" || len(f.requests()) != 6 {
		t.Fatalf("%+v %v (%d requests)", resp, err, len(f.requests()))
	}
	if _, err := ask(t, cl, choiceOf("first", "second")); err != nil || len(f.requests()) != 11 {
		t.Fatalf("not remembered: %v (%d requests)", err, len(f.requests()))
	}

	bad := newFake(t, func(int, seen) (int, any) { return 400, `{"error":"model not found"}` })
	cl, _ = newClient(t, bad.URL, "", jev.Capabilities{})
	if _, err := ask(t, cl, choiceOf("first", "second")); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("err = %v", err)
	}
	if p := cl.Backend.Capabilities().Probability; p != jev.ProbLogprob {
		t.Fatalf("a 400 for another reason was taken for no logprobs: %s", p)
	}
}

// More options than one request carries are answered as groups, then
// members; the probabilities cover every option and sum to 1 (Ask
// validates), and the pick is right — with 20 alternatives, and with a
// server that returns only 5 (Fireworks), learned from its first response.
func TestManyOptionsAreAnsweredHierarchically(t *testing.T) {
	for _, c := range []struct{ n, serverK, target int }{{40, 20, 31}, {255, 20, 137}, {255, 5, 200}, {27, 5, 0}} {
		t.Run(fmt.Sprintf("%d options, k=%d", c.n, c.serverK), func(t *testing.T) {
			ids := make([]string, c.n)
			for i := range ids {
				ids[i] = fmt.Sprintf("opt-%03d", i)
			}
			target := ids[c.target]
			f := newFake(t, oracle(t, target, c.serverK))
			cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
			for round := 0; round < 2; round++ {
				before := len(f.requests())
				resp, err := ask(t, cl, choiceOf(ids...))
				if err != nil {
					t.Fatal(err)
				}
				a := resp.Answers["q"]
				if a.Choice != target || len(a.Probabilities) != c.n {
					t.Fatalf("round %d: choice %q with %d probabilities", round, a.Choice, len(a.Probabilities))
				}
				sum := 0.0
				for _, p := range a.Probabilities {
					sum += p
				}
				if !near(sum, 1) {
					t.Fatalf("probabilities sum to %v", sum)
				}
				for i, r := range f.requests()[before:] {
					labels, _ := labelsOf(r.user())
					limit := 15
					if c.serverK == 5 && (round > 0 || i > 0) {
						limit = 2
					}
					if len(labels) > limit || len(labels) < 2 {
						t.Fatalf("round %d request %d offers %d labels", round, i, len(labels))
					}
				}
			}
		})
	}
}

// The usage of every request of a question is summed, and a cost the
// provider reports (OpenRouter's usage.cost) is passed on.
func TestUsageAndReportedCostAreSummed(t *testing.T) {
	f := newFake(t, func(int, seen) (int, any) {
		r := logprobReply(alt{"A", 0.9}, alt{"B", 0.1})
		r["usage"] = map[string]any{"prompt_tokens": 7, "completion_tokens": 1, "cost": 0.25}
		return 200, r
	})
	cl, db := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := cl.Ask(context.Background(), nil, map[string]jev.Question{"a": choiceOf("x", "y"), "b": choiceOf("x", "y")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 14 || resp.Usage.OutputTokens != 2 || resp.Usage.CostUSD == nil || !near(*resp.Usage.CostUSD, 0.5) {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	var cost float64
	if err := db.QueryRow(`SELECT cost_usd FROM jev_usage`).Scan(&cost); err != nil || !near(cost, 0.5) {
		t.Fatalf("recorded cost %v %v", cost, err)
	}
}
