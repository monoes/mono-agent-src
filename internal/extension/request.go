package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The extension→Go request channel.
//
// Everything else on this socket flows one way: Go sends a Command, the
// extension answers with a Response carrying the same id. That is no use to
// a browser that wants to ASK something — "is this URL already in the
// brain?" (RCL-02), "what does my brain say about this?" (RCL-05) — because
// the extension has no way to originate a message that expects an answer.
// acceptUnsolicitedCapture proved a message *can* arrive unasked-for, but it
// is a one-shot push with nothing coming back.
//
// So: a second, symmetric pair of frames, discriminated by a `kind` field
// that no existing frame carries.
//
//	extension → Go
//	  {"kind": "request", "id": "req-<uuid>", "method": "doc.lookup",
//	   "params": {"url": "https://…"}}
//
//	Go → extension, zero or more progress frames (only for methods that say
//	they emit them; they never settle the request)
//	  {"kind": "reply", "id": "req-<uuid>",
//	   "progress": {"stage": "searching", "detail": "12 documents"}}
//
//	Go → extension, exactly one settling frame
//	  {"kind": "reply", "id": "req-<uuid>", "ok": true, "data": {…}}
//	  {"kind": "reply", "id": "req-<uuid>", "ok": false,
//	   "error": "…", "code": "unavailable"}
//
// `kind` is the whole compatibility story. A Command has `type`+`params`, a
// Response has `id`+`success`; neither has ever had a `kind`, so an older
// peer on either end sees a frame it does not recognise and ignores it —
// which is exactly the right behaviour, since the requester times out and
// falls back. See isRequestFrame for the read-loop side of that.
//
// Rules this channel holds itself to, because the browser is on the other
// end of it and page loads must never wait on this process:
//
//   - every request is answered exactly once, including when its handler
//     panics, blocks past its deadline, or names a method nothing registered;
//   - a request is never run on the read loop — a handler that shells out to
//     monomind for two seconds would otherwise stall the whole extension
//     connection, captures included;
//   - concurrency is bounded, and the overflow is a fast "busy" reply rather
//     than an unbounded goroutine fan-out a compromised page could drive.

// Frame kinds for the request channel.
const (
	KindRequest = "request"
	KindReply   = "reply"
)

// Request method names. Handlers for these are registered by
// registerBuiltinHandlers; see knowledge.go.
const (
	// MethodDocLookup answers "is this URL already saved?" (RCL-02).
	MethodDocLookup = "doc.lookup"
	// MethodDocAsk answers a free-text question from the captures
	// (RCL-05). Emits progress frames.
	MethodDocAsk = "doc.ask"
	// MethodDocRelated is `doc related` over the channel — what the
	// already-saved panel shows underneath the entry.
	MethodDocRelated = "doc.related"
	// MethodPing is the channel's own liveness check. It touches nothing,
	// which makes it the right thing for the extension to probe with
	// before deciding the backend can answer questions at all.
	MethodPing = "ping"
)

// Reply error codes. The extension branches on these, so they are part of
// the contract: "unavailable" means "the backend is here but cannot answer
// this right now" (monomind missing, store empty) and is shown as a quiet
// absence; anything else is a real error worth surfacing.
const (
	CodeUnknownMethod = "unknown_method"
	CodeUnavailable   = "unavailable"
	CodeTimeout       = "timeout"
	CodeBusy          = "busy"
	CodeInternal      = "internal"
	CodeAccountLocked = "account_locked" // no valid monoes.me login: see account_door.go
	CodeForbidden     = "forbidden"      // the browser's token may not act for the profile the request names: see allowed_profiles.go
)

// requestTimeout bounds a handler that does not set its own. Generous
// because the handlers shell out to a Node CLI whose startup alone is a
// couple of hundred milliseconds, but far short of the extension's own
// patience so the timeout that fires is this one, with a code attached.
const requestTimeout = 20 * time.Second

// maxInflightRequests bounds how many handlers run at once. The extension
// debounces and caches per tab, so this only has to absorb a burst of tab
// switches; past it, callers get an immediate "busy" instead of a queue
// that grows without limit.
const maxInflightRequests = 8

// Request is one extension-initiated question.
type Request struct {
	Kind   string         `json:"kind"`
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`

	// Origin is the browser that asked, filled in by the server and never
	// read off the wire. A handler uses it to answer "for this browser",
	// e.g. profile.list defaulting to the profile the browser is bound to.
	Origin ConnInfo `json:"-"`
}

// Reply is one frame back. A frame with Progress set is informational and
// does not settle the request; a frame without it settles it exactly once.
type Reply struct {
	Kind     string    `json:"kind"`
	ID       string    `json:"id"`
	OK       bool      `json:"ok,omitempty"`
	Data     any       `json:"data,omitempty"`
	Error    string    `json:"error,omitempty"`
	Code     string    `json:"code,omitempty"`
	Progress *Progress `json:"progress,omitempty"`
}

// Progress is a "still working" note for a method slow enough that the
// panel would otherwise look frozen (RCL-05).
type Progress struct {
	Stage  string `json:"stage"`
	Detail string `json:"detail,omitempty"`
}

// ProgressFunc is handed to a handler so it can report where it has got to.
// It is always non-nil and never blocks: a dropped progress frame is
// cosmetic, and must not be able to stall the work it is describing.
type ProgressFunc func(stage, detail string)

// RequestHandler answers one method. Returning a RequestError carries a
// code through to the extension; any other error arrives as CodeInternal.
type RequestHandler func(ctx context.Context, req *Request, progress ProgressFunc) (any, error)

// RequestError is an error with a wire code attached.
type RequestError struct {
	Code string
	Err  error
	// Data rides along on the failure reply (e.g. the conflicting keys of
	// a selector_conflict); nil for most errors.
	Data any
}

func (e *RequestError) Error() string { return e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

// Unavailable is the error a handler returns when the backend is present
// but cannot answer — monomind is not installed, the brain is empty. The
// extension renders these as "no answer", never as a failure.
func Unavailable(format string, args ...any) error {
	return &RequestError{Code: CodeUnavailable, Err: fmt.Errorf(format, args...)}
}

// String reads a string param, trimmed. Missing or wrong-typed is "".
func (r *Request) String(key string) string {
	if r.Params == nil {
		return ""
	}
	s, _ := r.Params[key].(string)
	return strings.TrimSpace(s)
}

// Int reads a numeric param, falling back to def. JSON numbers arrive as
// float64; a string that parses as a number is accepted too, because the
// value came out of a browser.
func (r *Request) Int(key string, def int) int {
	if r.Params == nil {
		return def
	}
	switch v := r.Params[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// HandleRequest registers (or replaces) the handler for one method. Safe to
// call while the server is running.
func (s *Server) HandleRequest(method string, h RequestHandler) {
	s.handlerMu.Lock()
	defer s.handlerMu.Unlock()
	if s.handlers == nil {
		s.handlers = make(map[string]RequestHandler)
	}
	s.handlers[method] = h
}

// HandleRequestWithTimeout registers a handler whose requests get their own
// deadline instead of requestTimeout — for methods that legitimately run
// for minutes (record.analyze shells out to an AI runner).
func (s *Server) HandleRequestWithTimeout(method string, h RequestHandler, timeout time.Duration) {
	s.HandleRequest(method, h)
	s.handlerMu.Lock()
	defer s.handlerMu.Unlock()
	if s.methodTimeouts == nil {
		s.methodTimeouts = make(map[string]time.Duration)
	}
	s.methodTimeouts[method] = timeout
}

func (s *Server) handlerFor(method string) (RequestHandler, bool) {
	s.handlerMu.Lock()
	defer s.handlerMu.Unlock()
	h, ok := s.handlers[method]
	return h, ok
}

// RequestMethods lists the registered method names. The extension asks for
// this once per connection so a newer extension talking to an older backend
// can hide a panel instead of timing out against it.
func (s *Server) RequestMethods() []string {
	s.handlerMu.Lock()
	defer s.handlerMu.Unlock()
	out := make([]string, 0, len(s.handlers))
	for m := range s.handlers {
		out = append(out, m)
	}
	return out
}

// registerBuiltinHandlers installs the handlers every server has. Called
// from NewServer, so the channel works without any wiring at the call site —
// a channel that has to be switched on somewhere is a channel that is off.
func (s *Server) registerBuiltinHandlers() {
	s.HandleRequest(MethodPing, func(context.Context, *Request, ProgressFunc) (any, error) {
		return map[string]any{
			"pong":    true,
			"methods": s.RequestMethods(),
			"account": accountSummary(),
		}, nil
	})
	registerKnowledgeHandlers(s)
	registerRecordHandlers(s)
	registerCoderHandlers(s)
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

// isRequestFrame reports whether a raw frame from the extension is a
// request rather than a response to something this process sent. Cheap by
// design: it decodes one field, and only frames that claim KindRequest are
// decoded again as a Request.
func isRequestFrame(msg []byte) bool {
	return frameKind(msg) == KindRequest
}

// serveRequest runs one request to completion and settles it. Called from
// the read loop, so the actual work is handed to a goroutine — this
// function must return promptly no matter what the handler does.
func (s *Server) serveRequest(c *extConn, msg []byte) {
	var req Request
	if err := json.Unmarshal(msg, &req); err != nil {
		// Nothing to reply to: without an id, a reply could not be
		// correlated. Logged without the payload, like readLoop's own
		// decode failure, because a request may quote page text.
		s.logger.Error().Err(err).Int("len", len(msg)).Msg("invalid request JSON from extension")
		return
	}
	if req.ID == "" {
		s.logger.Warn().Str("method", req.Method).Msg("extension request with no id, dropped")
		return
	}
	req.Origin = c.info()

	if req.Method != MethodPing && accountRefuses() { // before the lookup: see account_door.go
		s.replyError(c, req.ID, CodeAccountLocked, errAccountLocked)
		return
	}

	// A restricted browser may act only for its allowed profiles: a request
	// that names another one, or none at all, is refused before any handler
	// (chat.send, vault.save, task add... all name their profile in params).
	if req.Method != MethodPing && !c.servesNow(req.String("profile")) {
		s.replyError(c, req.ID, CodeForbidden, fmt.Errorf("this browser's token may not act for profile %q: change what it may run with `monoagentcli extension browsers allow`", req.String("profile")))
		return
	}

	handler, ok := s.handlerFor(req.Method)
	if !ok {
		s.replyError(c, req.ID, CodeUnknownMethod, fmt.Errorf("unknown method %q", req.Method))
		return
	}

	// The semaphore is checked, not waited on: a caller that would have to
	// queue is better told so immediately, while it still has the option
	// of asking again.
	select {
	case s.requestSem() <- struct{}{}:
	default:
		s.replyError(c, req.ID, CodeBusy, fmt.Errorf("too many requests in flight (max %d)", maxInflightRequests))
		return
	}

	go func() {
		// The slot is released when runHandler returns, which it does at
		// the deadline whatever the handler is still doing. A wedged
		// handler costs one leaked goroutine; it does not cost a slot.
		defer func() { <-s.requestSem() }()
		defer func() {
			// A panic in the dispatch machinery itself must not take the
			// server down, and must still settle the request — an
			// extension waiting on a reply that never comes is the one
			// failure mode this channel cannot recover from on its own.
			// The handler's own panic is caught beside it, in runHandler.
			if r := recover(); r != nil {
				s.logger.Error().Interface("panic", r).Str("method", req.Method).Msg("request dispatch panicked")
				s.replyError(c, req.ID, CodeInternal, fmt.Errorf("handler panicked"))
			}
		}()
		s.runHandler(c, handler, &req)
	}()
}

// handlerOutcome is one handler's return, carried off its own goroutine.
type handlerOutcome struct {
	data any
	err  error
}

// runHandler applies the deadline and turns the handler's return into
// exactly one settling frame.
//
// The handler runs on its own goroutine and runHandler races it against the
// deadline, because a context is a request and not a guarantee: a handler
// that never selects on ctx.Done() — parked on a channel, or on an exec
// whose stdout pipe some grandchild still holds open — would otherwise
// never settle its request and never give its in-flight slot back. Eight of
// those and the channel is busy for the life of the process.
//
// What this does not do is stop the handler: Go has no way to. A wedged
// handler leaks one goroutine, which is the cheaper of the two failures and
// the one the extension can recover from.
func (s *Server) runHandler(c *extConn, handler RequestHandler, req *Request) {
	base := s.ctx
	if base == nil {
		base = context.Background()
	}
	timeout := s.requestDeadline(req.Method)
	// Cancelling on the way out is what gives a handler that DOES watch its
	// context — every exec in knowledge.go — the signal to stop working on
	// an answer nobody is waiting for any more.
	ctx, cancel := context.WithTimeout(base, timeout)
	defer cancel()

	var settled atomic.Bool
	progress := func(stage, detail string) {
		// A handler that outlived its deadline is writing for a request the
		// extension has already been told about; its frames are dropped
		// rather than sent under an id nobody is holding.
		if settled.Load() {
			return
		}
		s.writeReply(c, &Reply{
			Kind:     KindReply,
			ID:       req.ID,
			Progress: &Progress{Stage: stage, Detail: detail},
		})
	}

	// Buffered: nothing reads this channel once the deadline has fired, and
	// a late handler must not block on the send forever.
	done := make(chan handlerOutcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error().Interface("panic", r).Str("method", req.Method).Msg("request handler panicked")
				done <- handlerOutcome{err: &RequestError{
					Code: CodeInternal, Err: fmt.Errorf("handler panicked")}}
			}
		}()
		data, err := handler(ctx, req, progress)
		done <- handlerOutcome{data: data, err: err}
	}()

	select {
	case out := <-done:
		settled.Store(true)
		if out.err != nil {
			code := CodeInternal
			var data any
			var re *RequestError
			if asRequestError(out.err, &re) {
				code, data = re.Code, re.Data
			} else if ctx.Err() != nil {
				code = CodeTimeout
			}
			s.writeReply(c, &Reply{Kind: KindReply, ID: req.ID, OK: false, Error: out.err.Error(), Code: code, Data: data})
			return
		}
		s.writeReply(c, &Reply{Kind: KindReply, ID: req.ID, OK: true, Data: out.data})
	case <-ctx.Done():
		settled.Store(true)
		s.logger.Warn().Str("method", req.Method).Str("id", req.ID).
			Dur("after", timeout).Msg("request handler outlived its deadline")
		s.replyError(c, req.ID, CodeTimeout,
			fmt.Errorf("%s did not answer within %s", req.Method, timeout))
	}
}

// SetRequestTimeout overrides how long a handler gets before its request is
// settled as a timeout. Tests call it, so proving the deadline holds does
// not cost requestTimeout of wall clock; production leaves it alone.
func (s *Server) SetRequestTimeout(d time.Duration) {
	s.handlerMu.Lock()
	defer s.handlerMu.Unlock()
	s.requestTimeout = d
}

func (s *Server) requestDeadline(method string) time.Duration {
	s.handlerMu.Lock()
	defer s.handlerMu.Unlock()
	if s.requestTimeout > 0 {
		return s.requestTimeout
	}
	if d := s.methodTimeouts[method]; d > 0 {
		return d
	}
	return requestTimeout
}

// asRequestError is errors.As specialised to *RequestError, kept local so
// this file does not have to import errors for one call.
func asRequestError(err error, target **RequestError) bool {
	for err != nil {
		if re, ok := err.(*RequestError); ok {
			*target = re
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func (s *Server) replyError(c *extConn, id, code string, err error) {
	s.writeReply(c, &Reply{Kind: KindReply, ID: id, OK: false, Error: err.Error(), Code: code})
}

// writeReply writes one frame back to the browser that asked. Failures are
// logged and swallowed: the socket being gone is the ordinary case (the
// browser closed, the worker was suspended), not an error anyone can act on.
func (s *Server) writeReply(c *extConn, reply *Reply) {
	data, err := json.Marshal(reply)
	if err != nil {
		s.logger.Error().Err(err).Msg("marshal reply")
		return
	}
	if c == nil {
		return
	}
	if err := c.write(data); err != nil {
		s.logger.Debug().Err(err).Str("id", reply.ID).Msg("could not write reply")
	}
}

// requestSem lazily builds the in-flight semaphore, so a Server assembled
// by hand in a test works without a constructor.
func (s *Server) requestSem() chan struct{} {
	s.semOnce.Do(func() { s.sem = make(chan struct{}, maxInflightRequests) })
	return s.sem
}

// handlerState is the request channel's slice of Server. Kept together here
// rather than spread through server.go's struct so the channel is one
// readable unit.
type handlerState struct {
	handlers  map[string]RequestHandler
	handlerMu sync.Mutex
	// requestTimeout overrides the requestTimeout constant; zero means the
	// constant. Guarded by handlerMu. See SetRequestTimeout.
	requestTimeout time.Duration
	// methodTimeouts are per-method deadlines (HandleRequestWithTimeout);
	// requestTimeout, when set, still wins. Guarded by handlerMu.
	methodTimeouts map[string]time.Duration
	sem            chan struct{}
	semOnce        sync.Once
}
