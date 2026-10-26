package jev

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("TYPESAFE_BASE_URL", srv.URL)
	c, err := NewClient("k", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func choiceQ(ids ...string) Question {
	c := map[string]any{}
	for _, id := range ids {
		c[id] = id + " option"
	}
	return Question{Type: TypeChoice, Criteria: c}
}

func TestNewClientNeedsKey(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	if _, err := NewClient("", ""); !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("err = %v, want ErrNoAPIKey", err)
	}
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	c, err := NewClient("", "")
	if err != nil || c.APIKey != "env-key" || c.Model != DefaultModel {
		t.Fatalf("NewClient = %+v, %v", c, err)
	}
}

func TestAskSendsOneRequestAndValidates(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var req Request
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Model != DefaultModel || len(req.Questions) != 3 {
			t.Errorf("request = %+v", req)
		}
		_, _ = w.Write([]byte(`{"model":"jev-1.13.0","usage":{"input_tokens":42},"answers":{
			"route":{"type":"choice","choice":"b","probabilities":{"a":0.1,"b":0.9},"confidence":0.8},
			"spam":{"type":"noul","noul":0.05},
			"urgency":{"type":"score","score":1.4,"probabilities":{"0":0.1,"1":0.4,"2":0.5},"confidence":0.3}}}`))
	})
	resp, err := c.Ask(context.Background(), map[string]any{"text": "hi"}, map[string]Question{
		"route":   choiceQ("a", "b"),
		"spam":    {Type: TypeNoul},
		"urgency": {Type: TypeScore, Criteria: []string{"low", "mid", "high"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answers["route"].Choice != "b" || resp.Usage.InputTokens != 42 || resp.Answers["spam"].Noul != 0.05 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestAskRetriesOverloadThenFails(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(529)
	})
	_, err := c.Ask(context.Background(), "s", map[string]Question{"q": choiceQ("a", "b")})
	if err == nil || calls.Load() != 3 {
		t.Fatalf("err=%v calls=%d, want an error after 3 attempts", err, calls.Load())
	}
}

func TestAskDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(422)
	})
	if _, err := c.Ask(context.Background(), "s", map[string]Question{"q": choiceQ("a")}); err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestAskRejectsMissingAnswer(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{}}`))
	})
	if _, err := c.Ask(context.Background(), "s", map[string]Question{"q": choiceQ("a")}); !errors.Is(err, ErrInvalidAnswer) {
		t.Fatalf("err = %v, want ErrInvalidAnswer", err)
	}
}

func TestValidateChoice(t *testing.T) {
	q := choiceQ("a", "b")
	ok := func() Answer {
		return Answer{Type: TypeChoice, Choice: "a", Probabilities: map[string]float64{"a": 0.7, "b": 0.3}, Confidence: 0.6}
	}
	if err := Validate(q, ok()); err != nil {
		t.Fatalf("valid answer rejected: %v", err)
	}
	cases := map[string]func(*Answer){
		"invented":   func(a *Answer) { a.Choice = "zzz" },
		"not argmax": func(a *Answer) { a.Choice = "b" },
		"nan":        func(a *Answer) { a.Probabilities["b"] = math.NaN() },
		"missing":    func(a *Answer) { delete(a.Probabilities, "b") },
		"extra":      func(a *Answer) { a.Probabilities["c"] = 0 },
		"negative":   func(a *Answer) { a.Probabilities["b"] = -0.3 },
		"sum":        func(a *Answer) { a.Probabilities["b"] = 0.9 },
		"confidence": func(a *Answer) { a.Confidence = 5 },
		"type":       func(a *Answer) { a.Type = TypeScore },
	}
	for name, mutate := range cases {
		a := ok()
		mutate(&a)
		if err := Validate(q, a); !errors.Is(err, ErrInvalidAnswer) {
			t.Errorf("%s: err = %v, want ErrInvalidAnswer", name, err)
		}
	}
}

func TestValidateNoulAndScore(t *testing.T) {
	if err := Validate(Question{Type: TypeNoul}, Answer{Noul: 1.2}); err == nil {
		t.Error("noul 1.2 accepted")
	}
	q := Question{Type: TypeScore, Criteria: []string{"lo", "hi"}}
	if err := Validate(q, Answer{Score: 3, Probabilities: map[string]float64{"0": 0.5, "1": 0.5}}); err == nil {
		t.Error("score beyond the rubric accepted")
	}
}

func TestAskRejectsMissingNoul(t *testing.T) {
	for name, body := range map[string]string{
		"absent": `{"answers":{"spam":{"type":"noul"}}}`,
		"null":   `{"answers":{"spam":{"type":"noul","noul":null}}}`,
	} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		if resp, err := c.Ask(context.Background(), "s", map[string]Question{"spam": {Type: TypeNoul}}); !errors.Is(err, ErrInvalidAnswer) || resp != nil {
			t.Errorf("%s: resp=%+v err=%v, want ErrInvalidAnswer", name, resp, err)
		}
	}
}

func TestNoulZeroIsEncodedAndAccepted(t *testing.T) {
	raw, err := json.Marshal(Answer{Type: TypeNoul, Noul: 0})
	if err != nil || string(raw) != `{"type":"noul","noul":0}` {
		t.Fatalf("marshal = %s, %v", raw, err)
	}
	var a Answer
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	if err := Validate(Question{Type: TypeNoul}, a); err != nil || a.Noul != 0 {
		t.Fatalf("round-tripped 0.0 noul: %+v, %v", a, err)
	}
	// Non-noul answers keep omitting a zero noul.
	if raw, _ := json.Marshal(Answer{Type: TypeChoice, Choice: "a"}); strings.Contains(string(raw), "noul") {
		t.Fatalf("choice answer encodes noul: %s", raw)
	}
}

func TestOnResultGetsUsageOnValidationFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-x","usage":{"input_tokens":77},"answers":{"spam":{"type":"noul","noul":3}}}`))
	})
	var got *Response
	var gotErr error
	c.OnResult = func(_ *Request, resp *Response, _ time.Duration, err error) { got, gotErr = resp, err }
	resp, err := c.Ask(context.Background(), "s", map[string]Question{"spam": {Type: TypeNoul}})
	if resp != nil || !errors.Is(err, ErrInvalidAnswer) {
		t.Fatalf("Ask = %+v, %v", resp, err)
	}
	if got == nil || got.Usage.InputTokens != 77 || !errors.Is(gotErr, ErrInvalidAnswer) {
		t.Fatalf("OnResult resp=%+v err=%v", got, gotErr)
	}
}

func TestAskRepairsAChoiceThatContradictsItsProbabilities(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{
			"operation":{"type":"choice","choice":"CLICK","probabilities":{"CLICK":0.2,"WAIT":0.8},"confidence":0.5},
			"bad":{"type":"choice","choice":"zzz","probabilities":{"a":0.2,"b":0.8},"confidence":0.5}}}`))
	})
	ops := choiceQ("CLICK", "WAIT")
	resp, err := c.Ask(context.Background(), nil, map[string]Question{"operation": ops})
	if err != nil {
		t.Fatalf("a self-contradicting choice failed the ask: %v", err)
	}
	if got := resp.Answers["operation"].Choice; got != "WAIT" {
		t.Fatalf("choice = %q, want the most probable option WAIT", got)
	}
	if _, err := c.Ask(context.Background(), nil, map[string]Question{"bad": choiceQ("a", "b")}); !errors.Is(err, ErrInvalidAnswer) {
		t.Fatalf("a choice that was never offered must still be rejected, err = %v", err)
	}
}
