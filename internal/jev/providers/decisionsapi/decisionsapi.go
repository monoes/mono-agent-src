// Package decisionsapi is the decision adapter for the two vendors' native
// decision APIs: OpenAI's Decisions API (provider kind "openai-decisions",
// POST /v1/decisions) and Google's Gemini Decisions API (kind
// "gemini-decisions", POST /v1beta/decisions). Both return calibrated
// probabilities, so answers report jev.ProbNative, and both take every
// question of a request in one call. The provider's base_url is the host (the
// default is the vendor's); the path is added here. A reply can only name an
// option the code sent: the helpers in jev/answers.go build every answer from
// a distribution over the question's own options.
package decisionsapi

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

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// The provider kinds this package registers.
const (
	KindOpenAI = "openai-decisions"
	KindGemini = "gemini-decisions"
)

const (
	defaultOpenAIHost  = "https://api.openai.com"
	defaultGeminiHost  = "https://generativelanguage.googleapis.com"
	defaultOpenAIModel = "gpt-6-luna"
	defaultGeminiModel = "gemini-decision-1"
	timeout            = 30 * time.Second
	maxBody            = 8 << 20
	maxRetryWait       = 5 * time.Second
	maxOptions         = 255
)

func init() {
	for kind, host := range map[string]string{KindOpenAI: defaultOpenAIHost, KindGemini: defaultGeminiHost} {
		jevconf.RegisterKind(kind, func(p jevconf.Provider, key string) (jev.Backend, error) { return New(kind, p, key) },
			jevconf.KindSpec{DefaultBaseURL: host, NeedsKey: true})
	}
}

// Backend answers through one vendor's decision API. Build it with New.
type Backend struct {
	kind, url, key, model string
	http                  *http.Client
	retries               int
	sleep                 func(ctx context.Context, d time.Duration) error
}

// New builds the backend of provider record p with its key. A base_url that
// already ends in the API version gets only "/decisions" added.
func New(kind string, p jevconf.Provider, key string) (*Backend, error) {
	host, model, version := defaultOpenAIHost, defaultOpenAIModel, "/v1"
	if kind == KindGemini {
		host, model, version = defaultGeminiHost, defaultGeminiModel, "/v1beta"
	} else if kind != KindOpenAI {
		return nil, fmt.Errorf("decisionsapi: unknown kind %q", kind)
	}
	if key == "" {
		return nil, fmt.Errorf("%s: an API key is required", kind)
	}
	if h := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/"); h != "" {
		host = strings.TrimSuffix(h, version)
	}
	if m := strings.TrimSpace(p.Model); m != "" {
		model = m
	}
	return &Backend{kind: kind, url: host + version + "/decisions", key: key, model: model,
		http: jev.KeyedHTTPClient(timeout), retries: 2, sleep: sleepCtx}, nil
}

// Capabilities implements jev.Backend.
func (b *Backend) Capabilities() jev.Capabilities {
	per := 400
	if b.kind == KindGemini {
		per = 1000
	}
	return jev.Capabilities{
		Kinds:       []string{jev.TypeChoice, jev.TypeNoul, jev.TypeScore},
		Probability: jev.ProbNative, MaxChoiceOptions: maxOptions, QuestionsPerRequest: per,
	}
}

// Answer implements jev.Backend: every question in one request.
func (b *Backend) Answer(ctx context.Context, req *jev.Request) (*jev.Response, error) {
	for id, q := range req.Questions {
		if q.Type == jev.TypeChoice && len(jev.OptionIDs(q)) > maxOptions {
			return nil, fmt.Errorf("%s: %w: %q has more than %d options", b.kind, jev.ErrUnsupported, id, maxOptions)
		}
	}
	model := req.Model
	if model == "" {
		model = b.model
	}
	var (
		body  []byte
		order []string
		err   error
	)
	if b.kind == KindGemini {
		body, order, err = encodeGemini(model, req)
	} else {
		body, order, err = encodeOpenAI(model, req)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: encode request: %w", b.kind, err)
	}
	raw, err := b.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var resp *jev.Response
	if b.kind == KindGemini {
		resp, err = decodeGemini(raw, req.Questions)
	} else {
		resp, err = decodeOpenAI(raw, order, req.Questions)
	}
	if resp != nil {
		resp.Probability = jev.ProbNative
		if resp.Model == "" {
			resp.Model = model
		}
	}
	return resp, err
}

// Probe sends one tiny yes/no question to prove the endpoint and key work.
func (b *Backend) Probe(ctx context.Context) (jev.Capabilities, error) {
	_, err := b.Answer(ctx, &jev.Request{Model: b.model, State: map[string]any{"text": "ping"},
		Questions: map[string]jev.Question{"ping": {Type: jev.TypeNoul, Instructions: "Is this a connectivity check?"}}})
	if err != nil {
		return jev.Capabilities{}, err
	}
	return b.Capabilities(), nil
}

// post sends body and returns the reply. A 408, 429 or 5xx is retried as
// often as the caller's budget says (jev.RetriesFrom), waiting what
// Retry-After says (at most 5s). The key never appears in an error and a
// reply body is never quoted, only its allow-listed error code.
func (b *Backend) post(ctx context.Context, body []byte) ([]byte, error) {
	budget, budgeted := jev.RetriesFrom(ctx)
	retries := b.retries
	if budgeted {
		retries = budget
	}
	backoff := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		raw, status, retryAfter, err := b.once(ctx, body)
		if err != nil {
			return nil, b.redact(err)
		}
		if status < 300 {
			return raw, nil
		}
		retryable := status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500
		if !retryable || attempt >= retries {
			return nil, errors.New(jev.HTTPError(b.kind, status, raw, b.key))
		}
		wait := backoff
		if s, err := strconv.ParseFloat(retryAfter, 64); err == nil && s >= 0 {
			wait = time.Duration(min(s, 3600) * float64(time.Second))
		}
		if err := b.sleep(ctx, min(wait, maxRetryWait)); err != nil {
			return nil, err
		}
		backoff *= 2
	}
}

func (b *Backend) once(ctx context.Context, body []byte) (raw []byte, status int, retryAfter string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", fmt.Errorf("%s: %w", b.kind, err)
	}
	if b.kind == KindGemini {
		req.Header.Set("x-goog-api-key", b.key)
	} else {
		req.Header.Set("Authorization", "Bearer "+b.key)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := b.http.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("%s: request failed: %w", b.kind, err)
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, 0, "", fmt.Errorf("%s: read response: %w", b.kind, err)
	}
	return raw, res.StatusCode, res.Header.Get("Retry-After"), nil
}

func (b *Backend) redact(err error) error {
	if err == nil || !strings.Contains(err.Error(), b.key) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), b.key, "***"))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func describe(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}
