package openaiapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/monomind"
)

// ChatRequest is the part of POST /v1/chat/completions this server reads.
// Fields it does not know (temperature, max_tokens, stop, …) are accepted by
// the decoder and ignored: agent exec has no such options.
type ChatRequest struct {
	Model           string            `json:"model"`
	Messages        []Message         `json:"messages"`
	Stream          bool              `json:"stream"`
	StreamOptions   *StreamOptions    `json:"stream_options"`
	ReasoningEffort string            `json:"reasoning_effort"`
	ResponseFormat  *ResponseFormat   `json:"response_format"`
	N               *int              `json:"n"`
	Logprobs        *bool             `json:"logprobs"`
	Audio           json.RawMessage   `json:"audio"`
	Tools           []json.RawMessage `json:"tools"`
	ToolChoice      json.RawMessage   `json:"tool_choice"`
	// ParallelToolCalls is accepted and treated as false: a response carries one
	// tool call, and the model asks for the rest in the next round.
	ParallelToolCalls *bool             `json:"parallel_tool_calls"`
	Functions         []json.RawMessage `json:"functions"`
	FunctionCall      json.RawMessage   `json:"function_call"`

	// toolDecls and toolPick are what validateChat made of Tools and ToolChoice.
	toolDecls []toolDecl
	toolPick  toolChoice
	// toolWire maps the name of a declared function that monomind knows by an alias to
	// the alias, and toolDeclared maps it back: only functions with an alias are in them.
	toolWire, toolDeclared map[string]string
}

// StreamOptions is the request's stream_options.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ResponseFormat is the request's response_format.
type ResponseFormat struct {
	Type string `json:"type"`
}

// Message is one chat message.
type Message struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
	// ToolCalls are the calls an assistant message made; ToolCallID is the call
	// a tool message answers.
	ToolCalls  []ToolCall `json:"tool_calls"`
	ToolCallID string     `json:"tool_call_id"`
}

// Content is a message's content: a string, or an array of parts of which
// only text parts are supported.
type Content struct {
	// Text is the string, or the text parts joined by newlines.
	Text string
	// Unsupported is the type of the first part that is not text ("unknown"
	// when it has no type), "" when every part is text.
	Unsupported      string
	UnsupportedIndex int
}

// UnmarshalJSON accepts null, a string or an array of parts.
func (c *Content) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || string(b) == "null":
		*c = Content{UnsupportedIndex: -1}
		return nil
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*c = Content{Text: s, UnsupportedIndex: -1}
		return nil
	case b[0] == '[':
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(b, &parts); err != nil {
			return err
		}
		out := Content{UnsupportedIndex: -1}
		var texts []string
		for i, p := range parts {
			if p.Type != "text" {
				if out.Unsupported == "" {
					out.Unsupported, out.UnsupportedIndex = p.Type, i
					if out.Unsupported == "" {
						out.Unsupported = "unknown"
					}
				}
				continue
			}
			texts = append(texts, p.Text)
		}
		out.Text = strings.Join(texts, "\n")
		*c = out
		return nil
	}
	return fmt.Errorf("message content must be a string or an array of content parts")
}

// completion is a non-streaming chat.completion response.
type completion struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []completionChoice `json:"choices"`
	Usage   *usage             `json:"usage,omitempty"`
}

type completionChoice struct {
	Index        int              `json:"index"`
	Message      assistantMessage `json:"message"`
	FinishReason string           `json:"finish_reason"`
}

type assistantMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chunk is one streamed chat.completion.chunk.
type chunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []chunkChoice `json:"choices"`
	Usage   *usage        `json:"usage,omitempty"`
}

type chunkChoice struct {
	Index        int     `json:"index"`
	Delta        delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type delta struct {
	Role      string          `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"`
	ToolCalls []deltaToolCall `json:"tool_calls,omitempty"`
}

func strPtr(s string) *string { return &s }

type usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	// CostUSD is what the runtime reported the turn cost, null when it reported none:
	// it is never estimated here.
	CostUSD *float64 `json:"cost_usd"`
}

// usageFrom reports the turn's token counts and cost, or nil when the runtime
// reported neither.
func usageFrom(res *monomind.TurnResult) *usage {
	if res == nil || (!res.HasInputTokens && !res.HasOutputTokens && !res.HasCostUSD) {
		return nil
	}
	u := &usage{
		PromptTokens:     res.InputTokens,
		CompletionTokens: res.OutputTokens,
		TotalTokens:      res.InputTokens + res.OutputTokens,
	}
	if res.HasCostUSD {
		c := res.CostUSD
		u.CostUSD = &c
	}
	return u
}

// modelObject is one entry of GET /v1/models, with a monoagent block that
// plain OpenAI clients ignore.
type modelObject struct {
	ID        string    `json:"id"`
	Object    string    `json:"object"`
	Created   int64     `json:"created"`
	OwnedBy   string    `json:"owned_by"`
	Monoagent modelMeta `json:"monoagent"`
}

type modelMeta struct {
	Runtime      string   `json:"runtime"`
	Model        string   `json:"model"`
	Label        string   `json:"label"`
	Confinement  string   `json:"confinement"`
	Validated    bool     `json:"validated"`
	Capabilities []string `json:"capabilities"`
}

type modelList struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}
