package apipkg

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/workflow"
)

// defaultWait bounds polls, repeated steps and event streams.
const defaultWait = 5 * time.Minute

// maxStreamLine caps one server-sent-event line.
const maxStreamLine = 16 << 20

// waitFor returns the wait: secTmpl rendered as seconds when it gives a
// number, else ms milliseconds, else defaultWait.
func (r *opRun) waitFor(ms int, secTmpl string, sc Scope, what string) (time.Duration, error) {
	if secTmpl != "" {
		v, err := renderString(secTmpl, sc)
		if err != nil {
			return 0, r.errf("%s: %w", what, err)
		}
		if s := strings.TrimSpace(stringify(v)); s != "" {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil || f <= 0 {
				return 0, r.errf("%s: %q is not a positive number of seconds", what, s)
			}
			return time.Duration(f * float64(time.Second)), nil
		}
	}
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond, nil
	}
	return defaultWait, nil
}

// repeatStep resends step st (first response resp) until its repeat.until
// renders true with res = the latest response.
func (r *opRun) repeatStep(ctx context.Context, st Step, sc Scope, resp *response) (*response, error) {
	rp := st.Repeat
	interval := time.Duration(rp.IntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Second
	}
	maxWait, err := r.waitFor(rp.MaxWaitMs, rp.MaxWaitSeconds, sc, "repeat.max_wait_seconds")
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(maxWait)
	once := st
	once.Repeat = nil
	for {
		rsc := sc
		rsc.Res = decodeBody(resp.body)
		done, err := renderString(rp.Until, rsc)
		if err != nil {
			return nil, r.errf("repeat.until: %w", err)
		}
		if b, _ := toBool(done); b == true {
			return resp, nil
		}
		if time.Now().After(deadline) {
			return nil, r.errf("repeat: not done after %s", maxWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		next, err := r.sendStep(ctx, once, sc)
		if err != nil {
			return nil, err
		}
		if next != nil {
			resp = next
		}
	}
}

// sendStream sends the request and reads its body as server-sent events
// until one matches stream.until; that event's data is the response.
func (r *opRun) sendStream(ctx context.Context, pp pageParams) (*response, error) {
	st := r.op.Stream
	timeout, err := r.waitFor(st.TimeoutMs, st.TimeoutSeconds, r.scope, "stream.timeout_seconds")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	b, err := r.buildRequest(ctx, pp)
	if err != nil {
		return nil, err
	}
	b.req.Header.Set("Accept", "text/event-stream")
	// The stream outlives the client's whole-request timeout; ctx bounds it.
	client := *r.client()
	client.Timeout = 0
	resp, err := client.Do(b.req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, r.errf("stream: no matching event within %s", timeout)
		}
		return nil, r.errf("%s %s: %w", b.req.Method, b.req.URL.Redacted(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyEcho))
		return nil, r.errf("HTTP %d: %s", resp.StatusCode, errorBodyEcho(body))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxStreamLine)
	var data []string
	dispatch := func() (*response, error) {
		if len(data) == 0 {
			return nil, nil
		}
		body := []byte(strings.Join(data, "\n"))
		data = data[:0]
		esc := r.scope
		esc.Res = decodeBody(body)
		ok, err := renderString(st.Until, esc)
		if err != nil {
			return nil, r.errf("stream.until: %w", err)
		}
		if hit, _ := toBool(ok); hit != true {
			return nil, nil
		}
		if err := r.checkErrorWhen(body, resp.Header); err != nil {
			return nil, err
		}
		return &response{status: resp.StatusCode, header: resp.Header, body: body, url: b.req.URL.String()}, nil
	}
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		switch {
		case line == "":
			if out, err := dispatch(); out != nil || err != nil {
				return out, err
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		// event:, id:, retry: and comment lines carry nothing res needs.
	}
	if out, err := dispatch(); out != nil || err != nil {
		return out, err
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, r.errf("stream: no matching event within %s", timeout)
	}
	if err := sc.Err(); err != nil {
		return nil, r.errf("reading event stream: %w", err)
	}
	return nil, r.errf("stream: the server closed the event stream before a matching event")
}

// fetchUploadURL downloads binary.upload_url (when it renders a URL) onto
// the run's item as its upload property, so the upload code sends it like
// any input binary. The file name defaults to the URL's last segment.
func (r *opRun) fetchUploadURL(ctx context.Context) error {
	b := r.op.Binary
	if b == nil || b.UploadURL == "" {
		return nil
	}
	v, err := renderText(b.UploadURL, r.scope)
	if err != nil {
		return r.errf("binary.upload_url: %w", err)
	}
	if v = strings.TrimSpace(v); v == "" {
		return nil
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return r.errf("binary.upload_url: %q is not an http(s) URL", v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v, nil)
	if err != nil {
		return r.errf("binary.upload_url: %w", err)
	}
	resp, err := r.client().Do(req)
	if err != nil {
		return r.errf("binary.upload_url: GET %s: %w", u.Redacted(), err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody+1))
	if err != nil {
		return r.errf("binary.upload_url: reading %s: %w", u.Redacted(), err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return r.errf("binary.upload_url: GET %s: HTTP %d: %s", u.Redacted(), resp.StatusCode, errorBodyEcho(data))
	}
	if len(data) > maxResponseBody {
		return r.errf("binary.upload_url: the file exceeds %d MiB", maxResponseBody>>20)
	}
	prop := r.uploadProperty()
	it := workflow.Item{JSON: make(map[string]any, len(r.item.JSON)+2), Binary: make(map[string][]byte, len(r.item.Binary)+1)}
	for k, x := range r.item.JSON {
		it.JSON[k] = x
	}
	for k, x := range r.item.Binary {
		it.Binary[k] = x
	}
	it.Binary[prop] = data
	if name := path.Base(u.Path); name != "." && name != "/" {
		it.JSON["file_name"] = name
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		it.JSON["mime_type"] = ct
	}
	delete(it.JSON, prop) // a stale base64 field must not shadow the fetched file
	r.item = it
	return nil
}
