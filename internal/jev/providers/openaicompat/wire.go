package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/jev"
)

// The chat-completions wire, read defensively: llama.cpp and older servers
// shape log-probabilities differently, and any shape this cannot read is
// "no log-probabilities", never an error.

const maxBody = 8 << 20

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string    `json:"model,omitempty"`
	Messages  []message `json:"messages"`
	MaxTokens int       `json:"max_tokens,omitempty"`
	// MaxCompletionTokens replaces MaxTokens for a model that refuses it
	// (OpenAI's reasoning models): see Backend.adapt.
	MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
	// Temperature is nil only for a model that refuses the parameter.
	Temperature *float64 `json:"temperature,omitempty"`
	Logprobs    bool     `json:"logprobs,omitempty"`
	TopLogprobs int      `json:"top_logprobs,omitempty"`
}

type chatResponse struct {
	// Model is the model the server says answered.
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Text     string          `json:"text"`
		Logprobs json.RawMessage `json:"logprobs"`
	} `json:"choices"`
	// CompletionProbabilities is llama.cpp's own shape.
	CompletionProbabilities []struct {
		Probs       []json.RawMessage `json:"probs"`
		TopLogprobs []json.RawMessage `json:"top_logprobs"`
	} `json:"completion_probabilities"`
	Usage struct {
		PromptTokens     int      `json:"prompt_tokens"`
		CompletionTokens int      `json:"completion_tokens"`
		Cost             *float64 `json:"cost"` // OpenRouter
	} `json:"usage"`
	// long is set when the request allowed more than one token
	// (max_completion_tokens): its text must then be exactly a label.
	long bool
}

// text is the generated text ("" when there is none).
func (r *chatResponse) text() string {
	if len(r.Choices) == 0 {
		return ""
	}
	c := r.Choices[0]
	var s string
	if json.Unmarshal(c.Message.Content, &s) == nil && s != "" {
		return s
	}
	return c.Text
}

// top is the first generated token's alternatives, nil when the response
// carries none in a shape this reads.
func (r *chatResponse) top() []jev.TokenLogprob {
	if len(r.Choices) > 0 && len(r.Choices[0].Logprobs) > 0 {
		var lp struct {
			Content []struct {
				Token       *string           `json:"token"`
				Logprob     *float64          `json:"logprob"`
				TopLogprobs []json.RawMessage `json:"top_logprobs"`
			} `json:"content"`
			// The legacy completions shape: one {token: logprob} map per token.
			TopLogprobs []map[string]float64 `json:"top_logprobs"`
		}
		if json.Unmarshal(r.Choices[0].Logprobs, &lp) == nil {
			if len(lp.Content) > 0 {
				c := lp.Content[0]
				if out := entries(c.TopLogprobs); len(out) > 0 {
					return out
				}
				if c.Token != nil && c.Logprob != nil {
					return []jev.TokenLogprob{{Token: *c.Token, Logprob: *c.Logprob}}
				}
			}
			if len(lp.TopLogprobs) > 0 {
				var out []jev.TokenLogprob
				for tok, l := range lp.TopLogprobs[0] {
					out = append(out, jev.TokenLogprob{Token: tok, Logprob: l})
				}
				if len(out) > 0 {
					return out
				}
			}
		}
	}
	if len(r.CompletionProbabilities) > 0 {
		c := r.CompletionProbabilities[0]
		if out := entries(c.TopLogprobs); len(out) > 0 {
			return out
		}
		if out := entries(c.Probs); len(out) > 0 {
			return out
		}
	}
	return nil
}

// entries reads alternatives as {token|tok_str, logprob|prob}, skipping any
// it cannot read.
func entries(raw []json.RawMessage) []jev.TokenLogprob {
	var out []jev.TokenLogprob
	for _, e := range raw {
		var v struct {
			Token   *string  `json:"token"`
			TokStr  *string  `json:"tok_str"`
			Logprob *float64 `json:"logprob"`
			Prob    *float64 `json:"prob"`
		}
		if json.Unmarshal(e, &v) != nil {
			continue
		}
		tok := v.Token
		if tok == nil {
			tok = v.TokStr
		}
		if tok == nil {
			continue
		}
		switch {
		case v.Logprob != nil:
			out = append(out, jev.TokenLogprob{Token: *tok, Logprob: *v.Logprob})
		case v.Prob != nil && *v.Prob > 0:
			out = append(out, jev.TokenLogprob{Token: *tok, Logprob: math.Log(*v.Prob)})
		}
	}
	return out
}

// httpError is a non-2xx reply. Its text is the status and an allow-listed
// error code (jev.HTTPError), never the body: body, with the key taken out,
// is only read to learn what was refused (trial.refused, namesTopLogprobs).
type httpError struct {
	status int
	body   string
	text   string
}

func (e *httpError) Error() string { return e.text }

// transportError is a request that got no reply. Its text is redacted; it
// unwraps to the cause, so a caller still tells a deadline or a
// cancellation (errors.Is) from a failure: jevconf.ErrorClass names a
// timeout, and a sample cancelled because another failed is not taken for
// the failure (firstErr).
type transportError struct {
	text string
	err  error
}

func (e *transportError) Error() string { return e.text }
func (e *transportError) Unwrap() error { return e.err }

// complete sends one chat completion and decodes the reply. A 400 that
// refuses max_tokens or temperature as unsupported (OpenAI's reasoning
// models) is sent again without that parameter, remembered once such a request
// succeeds (trial). Any other 400 is the caller's, a logprobs refusal
// included. An empty answer under completionBudget is asked once more with
// largeCompletionBudget, which is then kept.
func (b *Backend) complete(ctx context.Context, tr *trial, body chatRequest) (*chatResponse, error) {
	var spent struct{ PromptTokens, CompletionTokens int } // of an answer asked again
	for {
		sent := tr.adapt(b.base, body)
		enc, err := json.Marshal(sent)
		if err != nil {
			return nil, fmt.Errorf("openaicompat: encode the request: %w", err)
		}
		raw, err := b.do(ctx, http.MethodPost, "/chat/completions", enc)
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusBadRequest && tr.refused(sent, he.body) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var res chatResponse
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("%w: undecodable chat completion", jev.ErrInvalidAnswer)
		}
		res.long = sent.MaxCompletionTokens > 0
		res.Usage.PromptTokens += spent.PromptTokens
		res.Usage.CompletionTokens += spent.CompletionTokens
		if sent.MaxCompletionTokens == completionBudget && strings.TrimSpace(res.text()) == "" {
			// The model spent the whole budget reasoning: ask this once more
			// with the larger one, and keep it. The tokens spent are billed.
			tr.mu.Lock()
			tr.q.largeBudget = true
			tr.mu.Unlock()
			remember(b.base, body.Model, func(q *quirks) { q.largeBudget = true })
			spent.PromptTokens, spent.CompletionTokens = res.Usage.PromptTokens, res.Usage.CompletionTokens
			continue
		}
		tr.succeeded(b.base, sent)
		return &res, nil
	}
}

// completionBudget is max_completion_tokens for a model that refuses
// max_tokens: a reasoning model spends tokens before it answers. A model
// that spends all of them and answers nothing gets largeCompletionBudget.
const (
	completionBudget      = 16
	largeCompletionBudget = 256
)

// do sends a request to the provider's own base URL, retrying a 408, 429 or
// 5xx as often as the caller's budget says (jev.RetriesFrom; twice without
// one), honouring Retry-After up to 5s, as jev's TypeSafe client does.
func (b *Backend) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	retries := b.retries
	if n, ok := jev.RetriesFrom(ctx); ok {
		retries = n
	}
	backoff := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, b.base+path, rd)
		if err != nil {
			return nil, fmt.Errorf("openaicompat: %w", err)
		}
		if b.key != "" {
			req.Header.Set("Authorization", "Bearer "+b.key)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := b.http.Do(req)
		if err != nil {
			return nil, &transportError{text: "openaicompat: request failed: " + b.redact(err.Error()), err: err}
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, maxBody))
		res.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("openaicompat: read the response: %w", readErr)
		}
		if res.StatusCode < 300 {
			return raw, nil
		}
		retryable := res.StatusCode == 408 || res.StatusCode == 429 || res.StatusCode >= 500
		if !retryable || attempt >= retries {
			return nil, &httpError{status: res.StatusCode, body: b.snippet(raw), text: jev.HTTPError("openaicompat", res.StatusCode, raw, b.key)}
		}
		wait := backoff
		if s, err := strconv.ParseFloat(res.Header.Get("Retry-After"), 64); err == nil && s >= 0 {
			wait = time.Duration(s * float64(time.Second))
		}
		wait = min(wait, 5*time.Second)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		backoff *= 2
	}
}

// snippet is the start of a reply body with the key taken out (a server may
// echo the Authorization header), cut after the key is removed. It is only
// read (trial.refused, namesTopLogprobs), never put in an error.
func (b *Backend) snippet(raw []byte) string {
	s := []rune(b.redact(strings.TrimSpace(string(raw))))
	if len(s) > 300 {
		return string(s[:300]) + "…"
	}
	return string(s)
}

func (b *Backend) redact(s string) string {
	if b.key == "" {
		return s
	}
	return strings.ReplaceAll(s, b.key, "[redacted]")
}

// Models implements jev.ModelLister: GET {base_url}/models.
func (b *Backend) Models(ctx context.Context) ([]jev.Model, error) {
	raw, err := b.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("openaicompat: undecodable models list")
	}
	out := make([]jev.Model, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			out = append(out, jev.Model{ID: m.ID})
		}
	}
	return out, nil
}
