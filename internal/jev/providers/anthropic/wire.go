package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The Messages API wire format, and the one HTTP path every call takes.

const (
	apiVersion = "2023-06-01"
	// maxBody caps a response body: a forced tool call is a few hundred bytes.
	maxBody = 1 << 20
	// maxModelsBody caps the models list.
	maxModelsBody = 4 << 20
	maxRetryWait  = 5 * time.Second
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type messagesRequest struct {
	Model       string     `json:"model"`
	MaxTokens   int        `json:"max_tokens"`
	Temperature float64    `json:"temperature"`
	System      string     `json:"system"`
	Messages    []message  `json:"messages"`
	Tools       []tool     `json:"tools"`
	ToolChoice  toolChoice `json:"tool_choice"`
}

type messagesResponse struct {
	Model   string `json:"model"`
	Content []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// toolAnswer is the "answer" tool's input in resp, or "" when there is no
// such tool call or its input is not {"answer": "<string>"}.
func (r messagesResponse) toolAnswer() string {
	for _, c := range r.Content {
		if c.Type != "tool_use" || c.Name != toolName {
			continue
		}
		var in struct {
			Answer string `json:"answer"`
		}
		if json.Unmarshal(c.Input, &in) == nil {
			return in.Answer
		}
		return ""
	}
	return ""
}

// do sends one request to path (method GET when body is nil, else POST) and
// returns the response body. A 408, 429 or 5xx is retried b.retries times,
// waiting what Retry-After says (at most 5s). Every error is passed through
// b.redact, so the key never appears in one.
func (b *Backend) do(ctx context.Context, path string, body []byte, limit int64) ([]byte, error) {
	backoff := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		raw, status, retryAfter, err := b.once(ctx, path, body, limit)
		if err != nil {
			return nil, b.redact(err)
		}
		if status < 300 {
			return raw, nil
		}
		retryable := status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
		if !retryable || attempt >= b.retries {
			return nil, fmt.Errorf("anthropic: HTTP %d: %s", status, snippet(b.scrub(string(raw))))
		}
		wait := backoff
		if s, err := strconv.ParseFloat(retryAfter, 64); err == nil && s >= 0 {
			wait = time.Duration(s * float64(time.Second))
		}
		if wait > maxRetryWait {
			wait = maxRetryWait
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		backoff *= 2
	}
}

func (b *Backend) once(ctx context.Context, path string, body []byte, limit int64) (raw []byte, status int, retryAfter string, err error) {
	method, rd := http.MethodGet, io.Reader(nil)
	if body != nil {
		method, rd = http.MethodPost, bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+path, rd)
	if err != nil {
		return nil, 0, "", fmt.Errorf("anthropic: %w", err)
	}
	if b.key != "" {
		req.Header.Set("x-api-key", b.key)
	}
	req.Header.Set("anthropic-version", apiVersion)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	res, err := b.http.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, 0, "", fmt.Errorf("anthropic: read response: %w", err)
	}
	return raw, res.StatusCode, res.Header.Get("Retry-After"), nil
}

// redact takes the key out of err's text. Only a transport error reaches it
// (those carry no header values, so this is belt and braces); a key a
// server echoes in its body is scrubbed before the body is quoted.
func (b *Backend) redact(err error) error {
	if err == nil || b.key == "" || !strings.Contains(err.Error(), b.key) {
		return err
	}
	return errors.New(b.scrub(err.Error()))
}

// scrub takes the key out of s, before s is cut short (a cut key would
// survive as a prefix).
func (b *Backend) scrub(s string) string {
	if b.key == "" {
		return s
	}
	return strings.ReplaceAll(s, b.key, "***")
}

func snippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
