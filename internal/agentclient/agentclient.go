// Package agentclient is the client side of the daemon's agent API (internal/httpapi,
// /profiles/{profile}/agent/v1/...): what `monoagentcli` calls instead of opening the local
// database when MONOAGENT_API_URL and MONOAGENT_API_TOKEN are set (client mode).
//
// A failure never carries the token: it is scrubbed from every message this package builds.
package agentclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/credhttp"
)

// The environment pair that switches the CLI to client mode.
const (
	EnvURL   = "MONOAGENT_API_URL"
	EnvToken = "MONOAGENT_API_TOKEN"
)

// Codes of refusals that do not come from the task service.
const (
	// CodeAPIRefused: the API answered 401 or 403 without a task refusal (no, bad, expired or
	// unbound token).
	CodeAPIRefused = "agent_api_refused"
	// CodeAPIUnavailable: the API could not be reached or answered something that is not its.
	CodeAPIUnavailable = "agent_api_unavailable"
)

const (
	taskTokenHeader = "X-Aitask-Token"
	maxResponse     = 16 << 20
)

// Client calls the agent API with one scoped token.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// FromEnv is the client the environment asks for, or nil when it does not ask for one: both
// variables must be set. A set pair with an unusable URL still yields a client whose calls
// fail, so a bad URL can never turn client mode off.
func FromEnv() *Client {
	base, token := strings.TrimSpace(os.Getenv(EnvURL)), strings.TrimSpace(os.Getenv(EnvToken))
	if base == "" || token == "" {
		return nil
	}
	return New(base, token)
}

// New is a client for the API under base (http://host:port/profiles/ID) with token.
func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		// credhttp.Client adds the credential policy to ours, which runs first and follows no redirect
		// at all: the token goes to the URL it was given for, never to wherever that points on.
		http: credhttp.Client(&http.Client{
			Timeout:       60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}),
	}
}

// HTTPClient lets a test point the client at its own transport.
func (c *Client) HTTPClient() *http.Client { return c.http }

// scrub hides the bearer, and the task token when there is one, wherever they appear in s.
func (c *Client) scrub(s string, taskToken ...string) string {
	for _, t := range append([]string{c.token}, taskToken...) {
		if len(t) >= 6 {
			s = strings.ReplaceAll(s, t, "[token]")
		}
	}
	return aitask.RedactAgentTokens(s) // any agent token, whoever's
}

// scrubAny scrubs every string inside v, keys included.
func (c *Client) scrubAny(v any, taskToken string) any {
	switch t := v.(type) {
	case string:
		return c.scrub(t, taskToken)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = c.scrubAny(e, taskToken)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[c.scrub(k, taskToken)] = c.scrubAny(e, taskToken)
		}
		return out
	}
	return v
}

// secureTarget says whether credentials may be sent to u: https anywhere, plain http to this
// machine only.
func secureTarget(u *url.URL) bool {
	if u.User != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		switch strings.ToLower(u.Hostname()) {
		case "127.0.0.1", "::1", "localhost":
			return true
		}
	}
	return false
}

func (c *Client) unavailable(taskToken, format string, a ...any) error {
	return &aitask.Error{Code: CodeAPIUnavailable, Message: c.scrub(fmt.Sprintf(format, a...), taskToken)}
}

// do sends one request and returns the status and body.
func (c *Client) do(ctx context.Context, method, path, taskToken string, body []byte) (int, []byte, error) {
	u, err := url.Parse(c.base + path)
	if err != nil || !secureTarget(u) {
		return 0, nil, c.unavailable(taskToken, "%s must be an https URL, or http to 127.0.0.1, [::1] or localhost: the bearer and the task token are not sent anywhere else", EnvURL)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, c.unavailable(taskToken, "building the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if taskToken != "" {
		req.Header.Set(taskTokenHeader, taskToken)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL is the caller's own and not worth repeating
		}
		return 0, nil, c.unavailable(taskToken, "the agent API at %s cannot be reached: %v", c.base, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return 0, nil, c.unavailable(taskToken, "reading the agent API's answer: %v", err)
	}
	return resp.StatusCode, b, nil
}

// refusal turns a non-2xx answer into the *aitask.Error the commands print.
func (c *Client) refusal(status int, b []byte, taskToken string) error {
	if status >= 300 && status < 400 {
		return c.unavailable(taskToken, "the agent API answered with a redirect (HTTP %d), which is not followed", status)
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) == nil {
		doc, _ = c.scrubAny(doc, taskToken).(map[string]any) // the whole document, before anything is copied out of it
		if code, _ := doc["code"].(string); code != "" {
			msg, _ := doc["error"].(string)
			fields := map[string]any{}
			for k, v := range doc {
				if k != "code" && k != "error" {
					fields[k] = v
				}
			}
			return &aitask.Error{Code: code, Message: msg, Fields: fields}
		}
		if msg, _ := doc["error"].(string); msg != "" && status != http.StatusUnauthorized && status != http.StatusForbidden {
			return &aitask.Error{Code: aitask.CodeInvalidInput, Message: msg}
		}
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return &aitask.Error{Code: CodeAPIRefused,
			Message: fmt.Sprintf("the agent API refused this token (HTTP %d): it may be expired, revoked or not bound to this task", status)}
	}
	return c.unavailable(taskToken, "the agent API answered HTTP %d", status)
}

func (c *Client) call(ctx context.Context, method, path, taskToken string, body []byte, out any) error {
	status, b, err := c.do(ctx, method, path, taskToken, body)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return c.refusal(status, b, taskToken)
	}
	// What the API says is data: it never carries either credential on to the caller.
	b = aitask.ScrubJSON(b, func(s string) string { return c.scrub(s, taskToken) })
	if err := json.Unmarshal(b, out); err != nil {
		return c.unavailable(taskToken, "the agent API's answer is not what this version expects")
	}
	return nil
}

func taskPath(id, tail string) string { return "/agent/v1/aitask/" + url.PathEscape(id) + tail }

// Kinds lists the AI task kinds.
func (c *Client) Kinds(ctx context.Context) ([]aitask.KindInfo, error) {
	var out struct {
		Kinds []aitask.KindInfo `json:"kinds"`
	}
	err := c.call(ctx, http.MethodGet, "/agent/v1/aitask/kinds", "", nil, &out)
	return out.Kinds, err
}

// Task is `aitask status`.
func (c *Client) Task(ctx context.Context, id string) (aitask.Task, error) {
	var t aitask.Task
	err := c.call(ctx, http.MethodGet, taskPath(id, ""), "", nil, &t)
	return t, err
}

// Result is `aitask result`.
type Result struct {
	TaskID string          `json:"taskId"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

// Result reads a task's result.
func (c *Client) Result(ctx context.Context, id string) (Result, error) {
	var r Result
	err := c.call(ctx, http.MethodGet, taskPath(id, "/result"), "", nil, &r)
	return r, err
}

// Complete hands result back; taskToken is the task's own (MONOAGENT_AITASK_TOKEN).
func (c *Client) Complete(ctx context.Context, id, taskToken string, result []byte) (aitask.Task, error) {
	var t aitask.Task
	err := c.call(ctx, http.MethodPost, taskPath(id, "/complete"), taskToken, append([]byte{}, result...), &t)
	return t, err
}

// Fail marks the task failed.
func (c *Client) Fail(ctx context.Context, id, taskToken, reason string) (aitask.Task, error) {
	body, _ := json.Marshal(map[string]string{"reason": reason})
	var t aitask.Task
	err := c.call(ctx, http.MethodPost, taskPath(id, "/fail"), taskToken, body, &t)
	return t, err
}

// Cancel stops the task.
func (c *Client) Cancel(ctx context.Context, id, taskToken string) (aitask.Task, error) {
	var t aitask.Task
	err := c.call(ctx, http.MethodPost, taskPath(id, "/cancel"), taskToken, nil, &t)
	return t, err
}
