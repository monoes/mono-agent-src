package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIDecisionsAdapter(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/decisions" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("bad request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		// Answers come back in question order: names sort as a, b, c.
		io.WriteString(w, `{"model":"gpt-6-luna","usage":{"input_tokens":40,"output_tokens":0},"answers":[
		 {"type":"choice","name":"a","choice":"yes","confidence":0.9,"probabilities":[{"value":"yes","probability":0.8},{"value":"no","probability":0.2}]},
		 {"type":"score","name":"b","score":1,"confidence":0.7,"probabilities":[{"label":"low","probability":0.25,"value":0},{"label":"high","probability":0.75,"value":1}]},
		 {"type":"predicate","name":"c","probability":0.93}]}`)
	}))
	defer srv.Close()

	c, err := NewClient("k", "gpt-6-luna")
	if err != nil {
		t.Fatal(err)
	}
	c.BaseURL, c.Protocol = srv.URL, ProtocolOpenAI
	resp, err := c.Ask(context.Background(), map[string]any{"text": "hi"}, map[string]Question{
		"a": {Type: TypeChoice, Criteria: map[string]any{"yes": "agree", "no": "disagree"}, Instructions: "pick"},
		"b": {Type: TypeScore, Criteria: []string{"low", "high"}, Instructions: "rate"},
		"c": {Type: TypeNoul, Instructions: "is it urgent?"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ch, p := Top(resp.Answers["a"]); ch != "yes" || p != 0.8 {
		t.Fatalf("a = %s %v", ch, p)
	}
	if resp.Answers["b"].Score != 1 || resp.Answers["b"].Probabilities["1"] != 0.75 {
		t.Fatalf("b = %+v", resp.Answers["b"])
	}
	if resp.Answers["c"].Noul != 0.93 || resp.Usage.InputTokens != 40 {
		t.Fatalf("c = %+v usage %+v", resp.Answers["c"], resp.Usage)
	}
	qs := got["questions"].([]any)
	if len(qs) != 3 || qs[2].(map[string]any)["type"] != "predicate" || qs[0].(map[string]any)["name"] != "a" {
		t.Fatalf("questions sent = %v", qs)
	}
}

func TestOpenAIRefusalIsAnError(t *testing.T) {
	_, err := decodeOpenAI([]byte(`{"answers":[{"type":"refusal","name":"a"}]}`), []string{"a"}, map[string]Question{"a": {Type: TypeNoul}})
	if err == nil {
		t.Fatal("a refused question must fail the call")
	}
}

func TestGeminiDecisionsAdapter(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/decisions" || r.Header.Get("x-goog-api-key") != "k" || r.Header.Get("Authorization") != "" {
			t.Errorf("bad request %s key=%q auth=%q", r.URL.Path, r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		io.WriteString(w, `{"answers":{"d":{"type":"noul","noul":0.99,"confidence":0.99}},"usage":{"total_tokens":100,"total_input_tokens":100,"total_output_tokens":0}}`)
	}))
	defer srv.Close()
	c, _ := NewClient("k", "gemini-decision-1")
	c.BaseURL, c.Protocol = srv.URL, ProtocolGemini
	resp, err := c.Ask(context.Background(), map[string]any{"t": "cracked"}, map[string]Question{"d": {Type: TypeNoul, Instructions: "damaged?"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answers["d"].Noul != 0.99 || resp.Usage.InputTokens != 100 {
		t.Fatalf("resp = %+v", resp)
	}
	if s, ok := got["input"].(string); !ok || s != `{"t":"cracked"}` {
		t.Fatalf("input = %v", got["input"])
	}
}
