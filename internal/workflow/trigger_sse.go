package workflow

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/secrets"
)

// TriggerNodeTypeSSE is the Server-Sent Events trigger (n8n "SSE Trigger").
const TriggerNodeTypeSSE = "trigger.sse"

// SSETriggerSource keeps a text/event-stream connection open for each active
// trigger.sse node and fires one run per event, with the item
// {event, data, id}: data is the parsed JSON value when the event's data is
// valid JSON, else the text. A dropped or refused connection is retried,
// waiting the server's retry: time (3s until it sends one) and doubling it
// while connecting keeps failing, up to a minute; the reconnect sends
// Last-Event-ID. A 204 answer stops the trigger until it is activated again,
// as the SSE spec says.
type SSETriggerSource struct {
	// DB resolves @secret:<name> header values for the workflow's profile;
	// nil leaves them as written.
	DB *sql.DB
	// Client defaults to an http.Client without a timeout (the stream is
	// long-lived; the request ends when the trigger is deactivated).
	Client *http.Client
	Logf   func(format string, args ...interface{})

	minRetry, maxRetry time.Duration // tests shorten them
}

// NewSSETriggerSource returns the trigger.sse source; register it with
// WorkflowEngine.RegisterTriggerSource.
func NewSSETriggerSource(db *sql.DB, logf func(format string, args ...interface{})) *SSETriggerSource {
	return &SSETriggerSource{DB: db, Logf: logf}
}

func (s *SSETriggerSource) Activate(w *Workflow, node *WorkflowNode, fire func(items []Item)) (func(), error) {
	if node.Config == nil {
		if err := node.ParseConfig(); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
	}
	raw, _ := node.Config["url"].(string)
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("%s: \"url\" must be an http(s) URL, got %q", TriggerNodeTypeSSE, raw)
	}
	headers, err := sseHeaders(node.Config["headers"])
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	if s.DB != nil {
		// A stream of a workflow the user hasn't approved sends no secret to
		// its URL, which the chat or an agent may have chosen.
		rctx := ctx
		if ok, err := operatorApproved(ctx, NewSQLiteWorkflowStore(s.DB), w); !ok {
			rctx = secrets.RefuseResolving(ctx, unapprovedSecrets(withApprovalCheckError(ctx, err), ApproveHint(w.ProfileID, w.ID)))
		}
		for k, v := range headers {
			if strings.HasPrefix(v, "@secret:") {
				resolved, err := secrets.Resolve(rctx, s.DB, w.ProfileID, v)
				if err != nil {
					cancel()
					return nil, fmt.Errorf("%s: header %s: %w", TriggerNodeTypeSSE, k, err)
				}
				headers[k] = resolved
			}
		}
	}
	c := &sseConn{src: s, url: u.String(), headers: headers, label: fmt.Sprintf("workflow %s, node %s", w.ID, node.ID)}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.loop(ctx, func(it Item) { fire([]Item{it}) })
	}()
	return func() { cancel(); wg.Wait() }, nil
}

// sseHeaders reads "headers": an object, or that object as JSON text (what
// the editor's textarea saves).
func sseHeaders(v interface{}) (map[string]string, error) {
	if text, ok := v.(string); ok {
		if strings.TrimSpace(text) == "" {
			return map[string]string{}, nil
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			return nil, fmt.Errorf("%s: \"headers\" is not a JSON object: %w", TriggerNodeTypeSSE, err)
		}
		v = m
	}
	headers := map[string]string{}
	switch hm := v.(type) {
	case nil:
	case map[string]interface{}:
		for k, val := range hm {
			headers[k] = fmt.Sprint(val)
		}
	default:
		return nil, fmt.Errorf("%s: \"headers\" must be an object of name: value", TriggerNodeTypeSSE)
	}
	return headers, nil
}

// errSSEStop is a 204 answer: the server asks the client not to reconnect.
var errSSEStop = errors.New("server answered 204 No Content: not reconnecting")

// sseConn is one trigger node's stream and the state that survives
// reconnects: the last event id and the server's retry time.
type sseConn struct {
	src     *SSETriggerSource
	url     string
	headers map[string]string
	label   string

	lastID string
	retry  time.Duration // the server's retry:, 0 = none sent
}

func (c *sseConn) logf(format string, args ...interface{}) {
	if c.src.Logf != nil {
		c.src.Logf("%s (%s): "+format, append([]interface{}{TriggerNodeTypeSSE, c.label}, args...)...)
	}
}

func (c *sseConn) loop(ctx context.Context, emit func(Item)) {
	minRetry, maxRetry := c.src.minRetry, c.src.maxRetry
	if minRetry == 0 {
		minRetry = 3 * time.Second
	}
	if maxRetry == 0 {
		maxRetry = time.Minute
	}
	failures := 0
	for ctx.Err() == nil {
		got, err := c.stream(ctx, emit)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errSSEStop) {
			c.logf("%v", err)
			return
		}
		if err == nil {
			err = errors.New("stream closed")
		}
		if got {
			failures = 0
		}
		base := minRetry
		if c.retry > 0 {
			base = c.retry
		}
		wait := base
		for i := 0; i < failures && wait < maxRetry; i++ {
			wait *= 2
		}
		if wait > maxRetry && base <= maxRetry {
			wait = maxRetry
		}
		failures++
		c.logf("%v; reconnecting in %s", err, wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// stream makes one request and reads events until the body ends; got says
// whether any event arrived.
func (c *sseConn) stream(ctx context.Context, emit func(Item)) (got bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return false, err
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if c.lastID != "" {
		req.Header.Set("Last-Event-ID", c.lastID)
	}
	client := c.src.Client
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return false, errSSEStop
	}
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) //nolint:errcheck
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	p := &sseParser{lastID: c.lastID}
	err = p.read(bufio.NewReader(resp.Body), func(ev sseEvent) {
		got = true
		emit(ev.item())
	})
	c.lastID = p.lastID
	if p.retry > 0 {
		c.retry = p.retry
	}
	return got, err
}

type sseEvent struct{ event, data, id string }

func (ev sseEvent) item() Item {
	var data interface{} = ev.data
	var parsed interface{}
	if json.Unmarshal([]byte(ev.data), &parsed) == nil {
		data = parsed
	}
	return NewItem(map[string]interface{}{"event": ev.event, "data": data, "id": ev.id})
}

// sseParser follows the event-stream interpretation rules of the HTML spec:
// lines end in CRLF, LF or CR; a blank line dispatches the event; a line
// starting with ':' is a comment; an unfinished event at the end of the
// stream is dropped.
type sseParser struct {
	lastID string
	retry  time.Duration

	event   string
	data    strings.Builder
	hasData bool
	afterCR bool
}

func (p *sseParser) read(r *bufio.Reader, dispatch func(sseEvent)) error {
	var line []byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if p.afterCR {
			p.afterCR = false
			if b == '\n' { // the LF of a CRLF already ended the line
				continue
			}
		}
		if b != '\n' && b != '\r' {
			line = append(line, b)
			continue
		}
		p.afterCR = b == '\r'
		p.line(string(line), dispatch)
		line = line[:0]
	}
}

func (p *sseParser) line(line string, dispatch func(sseEvent)) {
	if line == "" {
		if p.hasData {
			data := strings.TrimSuffix(p.data.String(), "\n")
			event := p.event
			if event == "" {
				event = "message"
			}
			dispatch(sseEvent{event: event, data: data, id: p.lastID})
		}
		p.event, p.hasData = "", false
		p.data.Reset()
		return
	}
	if strings.HasPrefix(line, ":") {
		return
	}
	field, value, _ := strings.Cut(line, ":")
	value = strings.TrimPrefix(value, " ")
	switch field {
	case "event":
		p.event = value
	case "data":
		p.data.WriteString(value)
		p.data.WriteByte('\n')
		p.hasData = true
	case "id":
		if !strings.ContainsRune(value, 0) {
			p.lastID = value
		}
	case "retry":
		if ms, err := strconv.ParseUint(value, 10, 63); err == nil {
			p.retry = time.Duration(ms) * time.Millisecond
		}
	}
}
