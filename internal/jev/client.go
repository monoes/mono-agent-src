// Package jev is a client for TypeSafe's Jev "System One" API
// (https://docs.typesafe.ai/api): a model that answers typed questions about
// a JSON state — pick one of N options (choice), yes/no (noul), or a level on
// an ordered rubric (score) — with probabilities and a confidence, and never
// generates text. One request can carry many independent questions.
//
// Every answer is validated against the question that produced it before a
// caller sees it, so a malformed or out-of-set reply is an error, never an
// action. There is no official Go SDK; this talks to the HTTP API directly.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/credhttp"
)

// Question types accepted by /v1/systemone.
const (
	TypeChoice = "choice"
	TypeNoul   = "noul"
	TypeScore  = "score"
)

// DefaultModel tracks the newest Jev release.
const DefaultModel = "jev-latest"

// DefaultBaseURL is TypeSafe's API host; TYPESAFE_BASE_URL overrides it.
var DefaultBaseURL = "https://api.typesafe.ai"

// ErrNoAPIKey is returned when neither config nor TYPESAFE_API_KEY has a key.
var ErrNoAPIKey = errors.New("jev: no TypeSafe API key (set api_key, e.g. @secret:typesafe, or TYPESAFE_API_KEY)")

// ErrInvalidAnswer wraps every answer that fails validation.
var ErrInvalidAnswer = errors.New("jev: invalid answer")

// Question is one typed question. Criteria is a map[string]any of
// option → description for choice, an ordered []string for score, and an
// optional {"true": …, "false": …} map for noul.
type Question struct {
	Type         string `json:"type"`
	Criteria     any    `json:"criteria,omitempty"`
	Instructions any    `json:"instructions,omitempty"`
}

// Request is the /v1/systemone body.
type Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Answer is one question's answer. Choice/Probabilities/Confidence are set
// for choice; Noul for noul; Score/Legend/Probabilities/Confidence for score.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`

	// noulMissing is set by UnmarshalJSON when the decoded object had no
	// (or a null) "noul" field, so Validate can tell a missing noul from a
	// real 0.0. Answers built in Go code are never "missing".
	noulMissing bool
}

// answerAlias has Answer's fields without its JSON methods.
type answerAlias Answer

// MarshalJSON always encodes "noul" for a noul answer — a legitimate 0.0
// would otherwise be dropped by omitempty and read back as missing.
func (a Answer) MarshalJSON() ([]byte, error) {
	var noul *float64
	if a.Type == TypeNoul || a.Noul != 0 {
		n := a.Noul
		noul = &n
	}
	return json.Marshal(struct {
		answerAlias
		Noul *float64 `json:"noul,omitempty"`
	}{answerAlias(a), noul})
}

// UnmarshalJSON decodes an answer and records whether "noul" was present.
func (a *Answer) UnmarshalJSON(b []byte) error {
	aux := struct {
		*answerAlias
		Noul *float64 `json:"noul"`
	}{answerAlias: (*answerAlias)(a)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	a.Noul, a.noulMissing = 0, aux.Noul == nil
	if aux.Noul != nil {
		a.Noul = *aux.Noul
	}
	return nil
}

// Usage is the token accounting TypeSafe returns (output tokens are free).
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the /v1/systemone reply, with LatencyMS added by the client.
type Response struct {
	Model     string            `json:"model"`
	Answers   map[string]Answer `json:"answers"`
	Usage     Usage             `json:"usage"`
	LatencyMS int64             `json:"-"`
}

// Client calls /v1/systemone. The zero value is not usable; use NewClient.
type Client struct {
	APIKey  string
	BaseURL string
	Model   string
	HTTP    *http.Client
	// Retries is how many times a 408/429/5xx is retried (default 2, as the
	// official SDKs do). Transport errors are not retried: the caller decides.
	Retries int
	// OnResult, when set, is called once per Ask with the outcome — including
	// transport and validation failures (err set). resp is nil only when no
	// decodable 200 came back; after a validation failure it is the decoded
	// (rejected) response, so its Usage is still recorded. Usage
	// recorders hang off it; it must not retain req beyond the call.
	OnResult func(req *Request, resp *Response, latency time.Duration, err error)
}

// NewClient builds a client. An empty apiKey falls back to TYPESAFE_API_KEY,
// an empty model to TYPESAFE_DEFAULT_MODEL and then DefaultModel.
func NewClient(apiKey, model string) (*Client, error) {
	if apiKey == "" {
		apiKey = os.Getenv("TYPESAFE_API_KEY")
	}
	if apiKey == "" {
		return nil, ErrNoAPIKey
	}
	if model == "" {
		model = os.Getenv("TYPESAFE_DEFAULT_MODEL")
	}
	if model == "" {
		model = DefaultModel
	}
	base := os.Getenv("TYPESAFE_BASE_URL")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(base, "/"),
		Model:   model,
		HTTP:    credhttp.Client(&http.Client{Timeout: 25 * time.Second}), // a redirect elsewhere gets no API key
		Retries: 2,
	}, nil
}

// Ask sends the questions about state in one request and validates every
// answer against its question. Questions are independent: none sees another's
// answer, so ask everything you might need at once (speculative fan-out).
func (c *Client) Ask(ctx context.Context, state any, questions map[string]Question) (resp *Response, err error) {
	if len(questions) == 0 {
		return nil, errors.New("jev: no questions")
	}
	req := Request{Model: c.Model, State: state, Questions: questions}
	started := time.Now()
	// recorded is what OnResult sees: the decoded response whenever TypeSafe
	// answered (so a billed request that fails validation still records its
	// usage), even though the caller then gets nil.
	var recorded *Response
	if c.OnResult != nil {
		defer func() { c.OnResult(&req, recorded, time.Since(started), err) }()
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("jev: encode request: %w", err)
	}
	raw, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	var decoded Response
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("%w: undecodable response: %v", ErrInvalidAnswer, err)
	}
	decoded.LatencyMS = time.Since(started).Milliseconds()
	recorded = &decoded
	for id, q := range questions {
		a, ok := decoded.Answers[id]
		if !ok {
			return nil, fmt.Errorf("%w: no answer for %q", ErrInvalidAnswer, id)
		}
		a = repairChoice(q, a)
		decoded.Answers[id] = a
		if err := Validate(q, a); err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
	}
	return &decoded, nil
}

// repairChoice makes a choice answer agree with its own probabilities: when
// the named choice is an offered option but not the most probable one, the
// most probable one is the answer (Top gates on the probabilities, never on
// the stated choice). The model contradicting itself is not worth failing a
// whole run over; an answer whose probabilities are themselves invalid, or
// whose choice was never offered, is left for Validate to reject.
func repairChoice(q Question, a Answer) Answer {
	if q.Type != TypeChoice || a.Choice == "" || len(a.Probabilities) == 0 {
		return a
	}
	if _, offered := a.Probabilities[a.Choice]; !offered {
		return a
	}
	best, bestP := "", -1.0
	for id, p := range a.Probabilities {
		if p > bestP || (p == bestP && id < best) {
			best, bestP = id, p
		}
	}
	if bestP > a.Probabilities[a.Choice]+1e-6 {
		a.Choice = best
	}
	return a
}

// Top returns the most probable option and its probability — the gate value
// every surface compares with its threshold (plan D4). For a noul answer it
// returns ("true", noul).
func Top(a Answer) (string, float64) {
	if a.Type == TypeNoul || (a.Probabilities == nil && a.Choice == "") {
		return "true", a.Noul
	}
	best, bestP := "", -1.0
	for id, p := range a.Probabilities {
		if p > bestP || (p == bestP && id < best) {
			best, bestP = id, p
		}
	}
	if a.Choice != "" {
		if p, ok := a.Probabilities[a.Choice]; ok && p >= bestP {
			return a.Choice, p
		}
	}
	return best, bestP
}

// Margin is the gap between the two most probable options (0 when fewer
// than two). A small margin means the model was torn.
func Margin(a Answer) float64 {
	first, second := 0.0, 0.0
	for _, p := range a.Probabilities {
		if p > first {
			first, second = p, first
		} else if p > second {
			second = p
		}
	}
	return first - second
}

func (c *Client) post(ctx context.Context, body []byte) ([]byte, error) {
	backoff := 500 * time.Millisecond
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/systemone", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Content-Type", "application/json")
		res, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("jev: request failed: %w", err)
		}
		raw, readErr := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		res.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("jev: read response: %w", readErr)
		}
		if res.StatusCode < 300 {
			return raw, nil
		}
		retryable := res.StatusCode == 408 || res.StatusCode == 429 || res.StatusCode >= 500
		if !retryable || attempt >= c.Retries {
			return nil, fmt.Errorf("jev: HTTP %d: %s", res.StatusCode, snippet(raw))
		}
		wait := backoff
		if s, err := strconv.ParseFloat(res.Header.Get("Retry-After"), 64); err == nil && s >= 0 {
			wait = time.Duration(s * float64(time.Second))
		}
		if wait > 5*time.Second {
			wait = 5 * time.Second
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		backoff *= 2
	}
}

// Model is one entry of GET /v1/models.
type Model struct {
	ID string `json:"id"`
}

// Models lists the models the key can use (GET /v1/models). It accepts the
// OpenAI-style {"data":[{"id":…}]} list as well as a bare array or a
// {"models":[…]} wrapper, with entries as objects (id or name) or strings.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jev: request failed: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("jev: read response: %w", err)
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("jev: HTTP %d: %s", res.StatusCode, snippet(raw))
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		var wrapped struct {
			Data   []json.RawMessage `json:"data"`
			Models []json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, fmt.Errorf("jev: undecodable models list: %v", err)
		}
		entries = append(wrapped.Data, wrapped.Models...)
	}
	out := make([]Model, 0, len(entries))
	for _, e := range entries {
		var id string
		if json.Unmarshal(e, &id) != nil {
			var obj struct{ ID, Name string }
			if err := json.Unmarshal(e, &obj); err != nil {
				return nil, fmt.Errorf("jev: undecodable model entry: %v", err)
			}
			id = obj.ID
			if id == "" {
				id = obj.Name
			}
		}
		if id != "" {
			out = append(out, Model{ID: id})
		}
	}
	return out, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// Validate checks an answer against the question that produced it: the right
// type, a choice from the offered set that is also the most probable one,
// finite probabilities in [0,1] over exactly the offered options summing to
// ~1, and a confidence in [0,1].
func Validate(q Question, a Answer) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalidAnswer}, args...)...)
	}
	if a.Type != "" && a.Type != q.Type {
		return bad("type %q, want %q", a.Type, q.Type)
	}
	switch q.Type {
	case TypeNoul:
		if a.noulMissing {
			return bad("noul value missing")
		}
		if !unit(a.Noul) {
			return bad("noul %v outside [0,1]", a.Noul)
		}
		return nil
	case TypeChoice, TypeScore:
	default:
		return bad("unknown question type %q", q.Type)
	}
	ids := OptionIDs(q)
	if len(a.Probabilities) != len(ids) {
		return bad("%d probabilities for %d options", len(a.Probabilities), len(ids))
	}
	sum, top := 0.0, 0.0
	for _, id := range ids {
		p, ok := a.Probabilities[id]
		if !ok || !unit(p) {
			return bad("probability for %q missing or outside [0,1]", id)
		}
		sum += p
		top = math.Max(top, p)
	}
	if math.Abs(sum-1) > 0.02 {
		return bad("probabilities sum to %.3f", sum)
	}
	if !unit(a.Confidence) {
		return bad("confidence %v outside [0,1]", a.Confidence)
	}
	if q.Type == TypeChoice {
		p, ok := a.Probabilities[a.Choice]
		if !ok {
			return bad("choice %q was not offered", a.Choice)
		}
		if p < top-1e-6 {
			return bad("choice %q is not the most probable option", a.Choice)
		}
	} else if a.Score < 0 || a.Score > float64(len(ids)-1) {
		return bad("score %v outside 0..%d", a.Score, len(ids)-1)
	}
	return nil
}

// OptionIDs lists the answer keys a choice or score question can produce.
func OptionIDs(q Question) []string {
	switch c := q.Criteria.(type) {
	case map[string]any:
		ids := make([]string, 0, len(c))
		for id := range c {
			ids = append(ids, id)
		}
		return ids
	case map[string]string:
		ids := make([]string, 0, len(c))
		for id := range c {
			ids = append(ids, id)
		}
		return ids
	case []string:
		ids := make([]string, len(c))
		for i := range c {
			ids[i] = strconv.Itoa(i)
		}
		return ids
	case []any:
		ids := make([]string, len(c))
		for i := range c {
			ids[i] = strconv.Itoa(i)
		}
		return ids
	}
	return nil
}

func unit(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0 && f <= 1 }
