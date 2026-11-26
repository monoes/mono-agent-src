package openaicompat

import (
	"context"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
)

func TestOnlyARefusalOfTheParameterCounts(t *testing.T) {
	for _, tc := range []struct {
		body, param string
		want        bool
	}{
		{`{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead."}}`, "max_tokens", true},
		{`{"error":{"message":"max_tokens is deprecated, use max_completion_tokens"}}`, "max_tokens", true},
		{`{"error":{"message":"Unrecognized request argument supplied: max_tokens"}}`, "max_tokens", true},
		{`{"error":{"message":"nope","param":"max_tokens","code":"unsupported_parameter"}}`, "max_tokens", true},
		{`{"error":{"message":"Unsupported value: 'temperature' does not support 0 with this model."}}`, "temperature", true},
		{`{"error":{"message":"The temperature parameter is not supported."}}`, "temperature", true},
		{`{"error":{"message":"This model does not support temperature"}}`, "temperature", true},
		{`{"error":{"message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens (4999 in the messages, max_tokens: 1)."}}`, "max_tokens", false},
		{`{"error":{"message":"max_tokens is too large: 100000. This model supports at most 4096 completion tokens."}}`, "max_tokens", false},
		{`{"error":"max_tokens: invalid value"}`, "max_tokens", false},
		{`{"error":{"message":"temperature must be between 0 and 2"}}`, "temperature", false},
		{`{"error":{"message":"Unsupported parameter: 'seed'. max_tokens was 1."}}`, "max_tokens", false},
		{`{"error":{"message":"bad","param":"temperature","code":"invalid_value"}}`, "temperature", false},
		{`{"error":{"message":"Unsupported parameter: 'max_tokens'."}}`, "temperature", false},
	} {
		if got := namesUnsupported(tc.body, tc.param); got != tc.want {
			t.Errorf("%s refused by %s: %v, want %v", tc.param, tc.body, got, tc.want)
		}
	}
}

// refused is what is on record about the fake host for the model its clients ask for.
func refused(f *fake) quirks { return recall(f.URL, testModel) }

// A 400 that only mentions a parameter (a prompt too long) changes nothing: not the request, not what the next
// client sends.
func TestAnErrorThatMentionsAParameterIsNoRefusal(t *testing.T) {
	asOperator(t)
	f := newFake(t, func(int, seen) (int, any) {
		return 400, `{"error":{"message":"maximum context length is 10 tokens; you asked max_tokens 1 at temperature 0"}}`
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	if _, err := ask(t, cl, choiceOf("first", "second")); err == nil {
		t.Fatal("a refused request was answered")
	}
	for i, r := range f.requests() {
		if r.Body.MaxTokens != 1 || r.Body.MaxCompletionTokens != 0 || r.Raw["temperature"] == nil {
			t.Errorf("request %d was changed for it: %v", i, r.Raw)
		}
	}
	if q := refused(f); q != (quirks{}) {
		t.Errorf("remembered %+v", q)
	}
}

// A refusal is remembered only once a request without the parameter succeeds.
func TestARefusalWhoseRetryFailsIsNotRemembered(t *testing.T) {
	asOperator(t)
	f := newFake(t, func(_ int, r seen) (int, any) {
		if _, ok := r.Raw["max_tokens"]; ok {
			return 400, `{"error":{"message":"Unsupported parameter: 'max_tokens'."}}`
		}
		return 400, `{"error":{"message":"model not found"}}`
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	if _, err := ask(t, cl, choiceOf("first", "second")); err == nil {
		t.Fatal("a refused request was answered")
	}
	if q := refused(f); q != (quirks{}) {
		t.Errorf("remembered %+v", q)
	}
}

// A 400 to many alternatives that does not name them (the retry with fewer happens to succeed) caps nothing for
// later clients.
func TestAnUnnamed400DoesNotCapTheAlternatives(t *testing.T) {
	asOperator(t)
	answer := oracle(t, "second", 0)
	f := newFake(t, func(n int, r seen) (int, any) {
		if n == 0 {
			return 400, `{"error":{"message":"something odd"}}`
		}
		return answer(n, r)
	})
	askNew(t, f, "")
	if q := refused(f); q != (quirks{}) {
		t.Errorf("remembered %+v", q)
	}
}

// A probe asks with every parameter again: when that succeeds what was remembered is cleared, when the host
// refuses it again it is learned again, and a probe that fails changes nothing.
func TestAProbeThatSucceedsInFullClearsWhatWasRemembered(t *testing.T) {
	asOperator(t)
	f := reasoningHost(t)
	strict := f.reply
	// The probe's yes/no question is answered too, once its request is accepted.
	answering := func(refuse func(int, seen) (int, any)) func(int, seen) (int, any) {
		return func(n int, r seen) (int, any) {
			if !strings.Contains(r.user(), "greater than") {
				return refuse(n, r)
			}
			if _, ok := r.Raw["max_tokens"]; ok {
				return 400, `{"error":{"message":"Unsupported parameter: 'max_tokens'."}}`
			}
			return 200, logprobReply(alt{"Yes", 0.9}, alt{"No", 0.1})
		}
	}
	askNew(t, f, "")
	learned := refused(f)
	if !learned.completionTokens || !learned.noTemperature || learned.topLogprobs != retryTopK {
		t.Fatalf("the host's refusals were not remembered: %+v", learned)
	}
	probe := func() (first seen, err error) {
		before := len(f.requests())
		cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
		_, err = cl.Backend.(Prober).Probe(context.Background())
		return f.requests()[before], err
	}

	f.setReply(func(int, seen) (int, any) { return 401, `{"error":"no"}` })
	if _, err := probe(); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("probe: %v", err)
	}
	if q := refused(f); q != learned {
		t.Errorf("a failed probe changed what was remembered: %+v", q)
	}

	// The host still refuses max_tokens: the probe sends everything again and learns just that.
	f.setReply(answering(strict))
	first, err := probe()
	if err != nil || first.Body.MaxTokens != 1 || first.Raw["temperature"] == nil || first.Body.TopLogprobs != defaultTopK {
		t.Fatalf("probe: %v, first request %v", err, first.Raw)
	}
	if q := refused(f); q != (quirks{completionTokens: true}) {
		t.Errorf("after a probe refused max_tokens only: %+v", q)
	}

	// The host now accepts everything.
	f.setReply(func(int, seen) (int, any) { return 200, logprobReply(alt{"Yes", 0.9}, alt{"No", 0.1}) })
	if first, err = probe(); err != nil || first.Body.MaxTokens != 1 {
		t.Fatalf("probe: %v, first request %v", err, first.Raw)
	}
	if q := refused(f); q != (quirks{}) {
		t.Errorf("a probe that succeeded in full left %+v", q)
	}
	f.setReply(oracle(t, "second", 0))
	if sent := askNew(t, f, ""); sent[0].Body.MaxTokens != 1 || sent[0].Body.TopLogprobs != defaultTopK {
		t.Errorf("the next client sends %v", sent[0].Raw)
	}
}
