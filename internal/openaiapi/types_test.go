package openaiapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

func TestContentUnmarshal(t *testing.T) {
	cases := []struct {
		name, in, text, unsupported string
		index                       int
		wantErr                     bool
	}{
		{"string", `"hello"`, "hello", "", -1, false},
		{"null", `null`, "", "", -1, false},
		{"text parts", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb", "", -1, false},
		{"image part", `[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"x"}}]`, "look", "image_url", 1, false},
		{"part without type", `[{"text":"x"}]`, "", "unknown", 0, false},
		{"number", `42`, "", "", -1, true},
		{"object", `{"a":1}`, "", "", -1, true},
	}
	for _, c := range cases {
		var got Content
		err := json.Unmarshal([]byte(c.in), &got)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if got.Text != c.text || got.Unsupported != c.unsupported || got.UnsupportedIndex != c.index {
			t.Errorf("%s: got %+v", c.name, got)
		}
	}
}

func TestChatRequestDecodesWhatRealClientsSend(t *testing.T) {
	const body = `{
	  "model": "claude/sonnet", "stream": true, "temperature": 0.2, "top_p": 1, "max_tokens": 512,
	  "stop": ["\n\n"], "seed": 7, "user": "u-1", "metadata": {"a": "b"},
	  "stream_options": {"include_usage": true}, "reasoning_effort": "high",
	  "response_format": {"type": "json_object"},
	  "messages": [
	    {"role": "system", "content": "be brief"},
	    {"role": "user", "content": [{"type": "text", "text": "hi"}]}
	  ]
	}`
	var req ChatRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	if req.Model != "claude/sonnet" || !req.Stream || req.StreamOptions == nil || !req.StreamOptions.IncludeUsage ||
		req.ReasoningEffort != "high" || req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" ||
		len(req.Messages) != 2 || req.Messages[1].Content.Text != "hi" {
		t.Fatalf("decoded %+v", req)
	}
}

func TestCompletionJSONShape(t *testing.T) {
	c := completion{
		ID: "chatcmpl-1", Object: "chat.completion", Created: 100, Model: "claude/default",
		Choices: []completionChoice{{Index: 0, Message: assistantMessage{Role: "assistant", Content: "hi"}, FinishReason: "stop"}},
	}
	b, _ := json.Marshal(c)
	s := string(b)
	for _, want := range []string{`"object":"chat.completion"`, `"finish_reason":"stop"`, `"role":"assistant"`, `"content":"hi"`} {
		if !strings.Contains(s, want) {
			t.Errorf("%s missing %s", s, want)
		}
	}
	if strings.Contains(s, `"usage"`) {
		t.Errorf("usage must be omitted when the runtime reported no tokens: %s", s)
	}

	c.Usage = &usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}
	b, _ = json.Marshal(c)
	if !strings.Contains(string(b), `"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7,"cost_usd":null}`) {
		t.Errorf("usage shape: %s", b)
	}
}

func TestChunkJSONShape(t *testing.T) {
	role := chunk{ID: "c", Object: "chat.completion.chunk", Model: "m", Choices: []chunkChoice{{Delta: delta{Role: "assistant", Content: strPtr("")}}}}
	b, _ := json.Marshal(role)
	if !strings.Contains(string(b), `"delta":{"role":"assistant","content":""}`) || !strings.Contains(string(b), `"finish_reason":null`) {
		t.Errorf("role chunk: %s", b)
	}
	stop := "stop"
	last, _ := json.Marshal(chunk{Choices: []chunkChoice{{Delta: delta{}, FinishReason: &stop}}})
	if !strings.Contains(string(last), `"delta":{}`) || !strings.Contains(string(last), `"finish_reason":"stop"`) {
		t.Errorf("final chunk: %s", last)
	}
	usageOnly, _ := json.Marshal(chunk{Choices: []chunkChoice{}, Usage: &usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}})
	if !strings.Contains(string(usageOnly), `"choices":[]`) || !strings.Contains(string(usageOnly), `"total_tokens":3`) {
		t.Errorf("usage chunk: %s", usageOnly)
	}
}

func TestUsageFrom(t *testing.T) {
	if got := usageFrom(&monomind.TurnResult{}); got != nil {
		t.Errorf("no tokens reported must give nil usage, got %+v", got)
	}
	got := usageFrom(&monomind.TurnResult{InputTokens: 10, OutputTokens: 5, HasInputTokens: true, HasOutputTokens: true})
	if got == nil || got.PromptTokens != 10 || got.CompletionTokens != 5 || got.TotalTokens != 15 {
		t.Errorf("usageFrom = %+v", got)
	}
	// One of the two counts is enough to report.
	if got := usageFrom(&monomind.TurnResult{InputTokens: 4, HasInputTokens: true}); got == nil || got.TotalTokens != 4 {
		t.Errorf("input-only usage = %+v", got)
	}
}
