package hfzeroshot

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
)

// The zero-shot classification wire format, and the one HTTP path every
// call takes.

const (
	// maxBody caps a response body: one score per label.
	maxBody      = 1 << 20
	maxRetryWait = 5 * time.Second
	// A model that is still loading answers 503, often for longer than the
	// other retries allow: it gets loadingRetries retries (4 attempts), each
	// waiting what estimated_time or Retry-After says, at most maxLoadingWait.
	loadingRetries = 3
	maxLoadingWait = 20 * time.Second
)

type parameters struct {
	CandidateLabels []string `json:"candidate_labels"`
	MultiLabel      bool     `json:"multi_label"`
}

type classifyRequest struct {
	Inputs     string     `json:"inputs"`
	Parameters parameters `json:"parameters"`
}

// labelScores is the {"labels": [...], "scores": [...]} reply.
type labelScores struct {
	Labels []string  `json:"labels"`
	Scores []float64 `json:"scores"`
}

type labelScore struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

// decodeScores reads a reply in any of the shapes servers send:
// [{"label","score"},…], {"labels":[…],"scores":[…]}, or either wrapped in
// an outer array. A label that was not sent, a score that is not finite
// and ≥ 0, or mismatched lists wrap jev.ErrInvalidAnswer; the reply's own
// text is never quoted.
func decodeScores(raw []byte, sent []string) (map[string]float64, error) {
	return decodeShape(raw, sent, true)
}

func decodeShape(raw []byte, sent []string, unwrap bool) (map[string]float64, error) {
	bad := func(why string) error { return fmt.Errorf("%w: hf-zeroshot: %s", jev.ErrInvalidAnswer, why) }
	var pairs []labelScore
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var outer []json.RawMessage
		if err := json.Unmarshal(raw, &outer); err != nil {
			return nil, bad("undecodable response")
		}
		if len(outer) > 0 && wrapped(outer[0]) {
			if !unwrap {
				return nil, bad("undecodable response")
			}
			return decodeShape(outer[0], sent, false)
		}
		if err := json.Unmarshal(raw, &pairs); err != nil {
			return nil, bad("undecodable response")
		}
	} else {
		var ls labelScores
		if err := json.Unmarshal(raw, &ls); err != nil {
			return nil, bad("undecodable response")
		}
		if len(ls.Labels) != len(ls.Scores) {
			return nil, bad(fmt.Sprintf("%d labels and %d scores", len(ls.Labels), len(ls.Scores)))
		}
		for i := range ls.Labels {
			pairs = append(pairs, labelScore{ls.Labels[i], ls.Scores[i]})
		}
	}
	allowed := make(map[string]bool, len(sent))
	for _, l := range sent {
		allowed[l] = true
	}
	out := make(map[string]float64, len(pairs))
	for _, p := range pairs {
		if !allowed[p.Label] {
			return nil, bad("a label that was not sent")
		}
		out[p.Label] = p.Score
	}
	if len(out) == 0 {
		return nil, bad("no scores")
	}
	return out, nil
}

// wrapped reports whether first, the first element of an outer array, is a
// whole reply (a pair list, or a {"labels","scores"} object) rather than one pair.
func wrapped(first json.RawMessage) bool {
	first = bytes.TrimSpace(first)
	if len(first) > 0 && first[0] == '[' {
		return true
	}
	var obj struct {
		Labels []string `json:"labels"`
	}
	return json.Unmarshal(first, &obj) == nil && obj.Labels != nil
}

// post sends body to the endpoint and returns the response body. A 408,
// 429 or other 5xx is retried b.retries times, waiting what Retry-After
// says (at most 5s). A 503 (a model still loading) is retried
// loadingRetries times, waiting the longer of Retry-After and the body's
// estimated_time (at most 20s). Every wait ends with ctx. The key never
// appears in an error.
func (b *Backend) post(ctx context.Context, body []byte) ([]byte, error) {
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
		retries, maxWait := b.retries, maxRetryWait
		if status == http.StatusServiceUnavailable {
			retries, maxWait = max(retries, loadingRetries), maxLoadingWait
		}
		if !retryable || attempt >= retries {
			return nil, fmt.Errorf("hf-zeroshot: HTTP %d: %s", status, snippet(b.scrub(string(raw))))
		}
		wait := backoff
		if s, err := strconv.ParseFloat(retryAfter, 64); err == nil && s >= 0 {
			wait = seconds(s)
		}
		if status == http.StatusServiceUnavailable {
			var loading struct {
				EstimatedTime float64 `json:"estimated_time"`
			}
			if json.Unmarshal(raw, &loading) == nil && seconds(loading.EstimatedTime) > wait {
				wait = seconds(loading.EstimatedTime)
			}
		}
		if err := b.sleep(ctx, min(wait, maxWait)); err != nil {
			return nil, err
		}
		backoff *= 2
	}
}

// seconds is s seconds as a duration; a value that is not finite and
// positive is 0, and a huge one is capped by the caller's maximum wait.
func seconds(s float64) time.Duration {
	switch {
	case !(s > 0): // zero, negative or NaN
		return 0
	case s > 3600:
		return time.Hour
	}
	return time.Duration(s * float64(time.Second))
}

// sleepCtx waits d, or returns ctx's error when ctx ends first.
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

func (b *Backend) once(ctx context.Context, body []byte) (raw []byte, status int, retryAfter string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, "", fmt.Errorf("hf-zeroshot: %w", err)
	}
	if b.key != "" {
		req.Header.Set("Authorization", "Bearer "+b.key)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := b.http.Do(req)
	if err != nil {
		return nil, 0, "", fmt.Errorf("hf-zeroshot: request failed: %w", err)
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, 0, "", fmt.Errorf("hf-zeroshot: read response: %w", err)
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
