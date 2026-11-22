package decisionsapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

func backend(t *testing.T, kind string, h http.HandlerFunc) *Backend {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	b, err := New(kind, jevconf.Provider{BaseURL: srv.URL}, "k")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpenAIDecisions(t *testing.T) {
	var got map[string]any
	b := backend(t, KindOpenAI, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/decisions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("bad request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		io.WriteString(w, `{"model":"gpt-6-luna","usage":{"input_tokens":40,"output_tokens":0},"answers":[
		 {"type":"choice","name":"a","choice":"yes","confidence":0.9,"probabilities":[{"value":"yes","probability":0.8},{"value":"no","probability":0.2}]},
		 {"type":"score","name":"b","score":1,"confidence":0.7,"probabilities":[{"label":"low","probability":0.25,"value":0},{"label":"high","probability":0.75,"value":1}]},
		 {"type":"predicate","name":"c","probability":0.93}]}`)
	})
	resp, err := b.Answer(context.Background(), &jev.Request{State: map[string]any{"text": "hi"}, Questions: map[string]jev.Question{
		"a": {Type: jev.TypeChoice, Criteria: map[string]any{"yes": "agree", "no": "disagree"}, Instructions: "pick"},
		"b": {Type: jev.TypeScore, Criteria: []string{"low", "high"}, Instructions: "rate"},
		"c": {Type: jev.TypeNoul, Instructions: "urgent?"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if ch, p := jev.Top(resp.Answers["a"]); ch != "yes" || p < 0.79 || p > 0.81 {
		t.Fatalf("a = %s %v", ch, p)
	}
	if resp.Answers["b"].Probabilities["1"] < 0.74 || resp.Answers["c"].Noul != 0.93 || resp.Usage.InputTokens != 40 || resp.Probability != jev.ProbNative {
		t.Fatalf("resp = %+v", resp)
	}
	qs := got["questions"].([]any)
	if len(qs) != 3 || qs[2].(map[string]any)["type"] != "predicate" {
		t.Fatalf("questions sent = %v", qs)
	}
}

func TestOpenAIRefusalAndErrorsNeverQuoteTheBody(t *testing.T) {
	b := backend(t, KindOpenAI, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"answers":[{"type":"refusal","name":"a"}]}`)
	})
	q := map[string]jev.Question{"a": {Type: jev.TypeNoul}}
	if _, err := b.Answer(context.Background(), &jev.Request{Questions: q}); err == nil {
		t.Fatal("a refused question must fail the call")
	}
	b = backend(t, KindOpenAI, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"code":"approve_everything k"}}`)
	})
	_, err := b.Answer(context.Background(), &jev.Request{Questions: q})
	if err == nil || strings.Contains(err.Error(), "approve_everything") || strings.Contains(err.Error(), "k\"") {
		t.Fatalf("err = %v", err)
	}
}

func TestGeminiDecisionsWithAwkwardIDs(t *testing.T) {
	b := backend(t, KindGemini, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/decisions" || r.Header.Get("x-goog-api-key") != "k" || r.Header.Get("Authorization") != "" {
			t.Errorf("bad request %s", r.URL.Path)
		}
		var in struct {
			Input     string         `json:"input"`
			Questions map[string]any `json:"questions"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &in)
		answers := map[string]any{}
		for k, q := range in.Questions {
			if len(k) > 64 || geminiKeyBad.MatchString(k) {
				t.Errorf("key %q is not valid for Gemini", k)
			}
			if q.(map[string]any)["type"] == "noul" {
				answers[k] = map[string]any{"type": "noul", "noul": 0.7, "confidence": 0.7}
			} else {
				answers[k] = map[string]any{"type": "choice", "choice": "x", "probabilities": map[string]float64{"x": 0.6, "y": 0.4}, "confidence": 0.6}
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"answers": answers, "usage": map[string]int{"total_input_tokens": 100}})
	})
	long := strings.Repeat("a", 80)
	resp, err := b.Answer(context.Background(), &jev.Request{State: "s", Questions: map[string]jev.Question{
		"node:browser.jev/1": {Type: jev.TypeNoul},
		long:                 {Type: jev.TypeChoice, Criteria: map[string]any{"x": "", "y": ""}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answers["node:browser.jev/1"].Noul != 0.7 || resp.Answers[long].Choice != "x" || resp.Usage.InputTokens != 100 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestKeyRequiredAndKindsRegistered(t *testing.T) {
	if _, err := New(KindOpenAI, jevconf.Provider{}, ""); err == nil {
		t.Fatal("a missing key must be refused")
	}
	have := strings.Join(jevconf.Kinds(), ",")
	if !strings.Contains(have, KindOpenAI) || !strings.Contains(have, KindGemini) {
		t.Fatalf("kinds = %s", have)
	}
}
