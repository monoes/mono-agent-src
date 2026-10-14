package extension

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/capture"
)

// The page_capture wire contract (CLIP-01), implemented on the JS side in
// chrome-extension/:
//
//	Go → extension
//	  {"id": …, "type": "page_capture", "tabId": 12,
//	   "params": {"formats": ["mhtml","pdf","readable","screenshot"],
//	              "selection": false, "note": "", "tags": [], "collection": "",
//	              "mode": "video"}}
//	  tabId is omitted to mean "the active tab". mode is optional (see
//	  CaptureRequest.Mode); with it, formats is omitted unless set.
//
//	A capture whose meta carries "summarize": {"kind": "page"|"video"} is
//	written and acknowledged like any other; the after-write hook
//	(SetAfterWrite, wired to internal/capturesummary) then adds summary.md
//	and summary.json to its envelope in the background.
//
//	extension → Go, one chunk of an artifact too big for one frame (zero or
//	more, all before that capture's closing message)
//	  {"id": …, "success": true, "data": {
//	     "chunk": {"index": 0, "total": 4, "of": "page.mhtml"}, "bytes": …}}
//
//	extension → Go, the closing message (exactly one, always last)
//	  {"id": …, "success": true, "data": {"meta": {…}, "warnings": […],
//	   "final": true,
//	   "artifacts": [{"name": "readable.md", "encoding": "base64", "bytes": …},
//	                 {"name": "page.mhtml", "chunked": true, "chunks": 4}]}}
//
//	extension → Go, failure
//	  {"id": …, "success": false, "error": "…"}
//
// An artifact that was streamed carries no inline bytes in the closing
// message, only the count it was sent in — which is what lets a dropped
// frame fail the capture instead of truncating a file. The frame budget on
// the JS side is 8MiB of base64, well under the 32MiB read limit that would
// close the connection, so ordinary captures are a single message.
//
// A capture the extension queued while this process was down and flushed on
// reconnect uses the same messages with an id of the form `ext-<uuid>` that
// nothing here is waiting for, and carries "type": "page_capture" so it is
// recognizable even when it failed and has no data. See
// dispatch/isCaptureResponse.

// DefaultCaptureFormats is what `capture page` asks for when the caller
// names no formats.
var DefaultCaptureFormats = []string{"mhtml", "pdf", "readable", "screenshot"}

// DefaultCaptureTimeout is generous on purpose: a capture runs lazy-load
// prep, an MHTML snapshot, a print-to-PDF and a full-page screenshot on a
// page the user is actually looking at.
const DefaultCaptureTimeout = 120 * time.Second

// captureStreamBuffer bounds how many capture messages may be queued for a
// waiting CapturePage before the read loop starts dropping them. The
// receiver is a tight select loop, so this only has to absorb bursts.
const captureStreamBuffer = 128

// CaptureRequest is one page_capture command.
type CaptureRequest struct {
	// TabID is the tab to capture; zero means the active tab.
	TabID      int
	Formats    []string
	Selection  bool
	Note       string
	Tags       []string
	Collection string
	// Mode names one of the extension's capture modes (full, screenshot,
	// summary, video — chrome-extension/capture_modes.js). With a mode and
	// no Formats, the extension picks the mode's formats; "summary" and
	// "video" also ask the bridge for a summary.md.
	Mode string
	// SummaryRuntime and SummaryModel pick the AI that writes a "summary" or
	// "video" capture's summary.md (the extension stamps them into
	// meta.summarize). Empty means the bridge's default runtime and the
	// runtime's own default model; the bridge checks both before use.
	SummaryRuntime string
	SummaryModel   string
	Timeout        time.Duration
	// Inbox overrides where the envelope is written. It is deliberately
	// not part of the params the extension sees — the browser has no
	// business knowing about this machine's filesystem — so it rides the
	// relay hop as a query parameter instead (see
	// RemoteSender.CapturePage).
	Inbox string
	// Target picks the browser to capture in. The zero Target is the
	// default browser. Like Inbox, it rides the relay hop as query
	// parameters and never reaches the extension.
	Target Target
}

// Capturer is implemented by both bridges — the one that owns the extension
// connection and the one relaying through another process — so a caller
// does not care which it got.
type Capturer interface {
	CapturePage(req CaptureRequest) (*capture.Result, error)
}

func (r CaptureRequest) formats() []string {
	if len(r.Formats) == 0 {
		return DefaultCaptureFormats
	}
	return r.Formats
}

func (r CaptureRequest) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return DefaultCaptureTimeout
}

// command renders the request as the Command the extension receives.
func (r CaptureRequest) command() *Command {
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	params := map[string]interface{}{
		"selection":  r.Selection,
		"note":       r.Note,
		"tags":       tags,
		"collection": r.Collection,
	}
	// A mode brings its own formats; sending the defaults alongside it
	// would override them (capture_modes.js paramsFor keeps explicit ones).
	if r.Mode == "" || len(r.Formats) > 0 {
		params["formats"] = r.formats()
	}
	if r.Mode != "" {
		params["mode"] = r.Mode
	}
	if r.SummaryRuntime != "" {
		params["summaryRuntime"] = r.SummaryRuntime
	}
	if r.SummaryModel != "" {
		params["summaryModel"] = r.SummaryModel
	}
	return &Command{
		ID:     uuid.New().String(),
		Type:   CmdPageCapture,
		TabID:  r.TabID,
		Params: params,
	}
}

// captureRequestFromCommand recovers a CaptureRequest from a Command that
// arrived over the HTTP relay, where it has been through JSON and lost its
// Go types.
func captureRequestFromCommand(cmd *Command, timeout time.Duration, inbox string) CaptureRequest {
	req := CaptureRequest{TabID: cmd.TabID, Timeout: timeout, Inbox: inbox}
	p := cmd.Params
	if p == nil {
		return req
	}
	req.Formats = stringSlice(p["formats"])
	req.Tags = stringSlice(p["tags"])
	req.Selection, _ = p["selection"].(bool)
	req.Note, _ = p["note"].(string)
	req.Collection, _ = p["collection"].(string)
	req.Mode, _ = p["mode"].(string)
	req.SummaryRuntime, _ = p["summaryRuntime"].(string)
	req.SummaryModel, _ = p["summaryModel"].(string)
	return req
}

func stringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Server side
// ---------------------------------------------------------------------------

// captureParts lazily builds the per-server capture machinery and hands it
// back, so neither NewServer nor a Server built by hand in a test has to
// know about it. Everything here is reached from both the read loop and a
// waiting CapturePage, so it lives under pendMu like the rest of the
// dispatch state.
func (s *Server) captureParts() (*capture.Assembler, *capture.Writer) {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	if s.assembler == nil {
		s.assembler = capture.NewAssembler(s.captureSpoolOptions())
	}
	if s.captureWriter == nil {
		s.captureWriter = &capture.Writer{Inbox: s.captureInbox}
	}
	return s.assembler, s.captureWriter
}

// SetCaptureInbox overrides where captures land (default:
// capture.DefaultInbox, ~/.monomind/inbox).
func (s *Server) SetCaptureInbox(dir string) {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	s.captureInbox = dir
	s.captureWriter = &capture.Writer{Inbox: dir}
	// Drop the assembler so it is rebuilt spooling into the new inbox
	// rather than the old one. Safe because this is documented as a
	// before-the-first-capture call.
	s.assembler = nil
}

// SetCaptureOptions overrides the assembler's size, chunk-timeout and
// spool settings. Call before the first capture: an assembler mid-flight is
// replaced, not reconfigured.
func (s *Server) SetCaptureOptions(opts capture.Options) {
	s.pendMu.Lock()
	defer s.pendMu.Unlock()
	s.captureOptions = opts
	s.assembler = capture.NewAssembler(s.captureSpoolOptions())
}

// captureSpoolOptions defaults the chunk spool to the configured inbox, so
// streamed artifacts stage on the same filesystem they are about to land
// on — and, on a machine whose /tmp is a shared tmpfs, nowhere near it.
// Must be called with pendMu held.
func (s *Server) captureSpoolOptions() capture.Options {
	opts := s.captureOptions
	if opts.SpoolDir == "" {
		opts.SpoolDir = s.captureInbox // empty in turn means capture.DefaultInbox
	}
	return opts
}

// OnCapture registers a callback invoked for every capture that arrives
// without a command asking for it (CLIP-08's flushed queue), with the
// written result or the error that stopped it. Used for logging, and by
// tests to observe the unsolicited path.
func (s *Server) OnCapture(fn func(*capture.Result, error)) {
	s.pendMu.Lock()
	s.onCapture = fn
	s.pendMu.Unlock()
}

// SetAfterWrite registers a hook run for every envelope that lands on disk,
// requested or flushed, after it is written and before anyone waits on it
// further; while the account is locked it is not run (skipAfterWrite). It
// must return quickly: the summary job (internal/capturesummary) only queues
// work here, so a capture is acknowledged as fast as before summaries existed.
func (s *Server) SetAfterWrite(fn func(*capture.Result)) {
	s.pendMu.Lock()
	s.afterWrite = fn
	s.pendMu.Unlock()
}

// written runs the after-write hook, if any. A panicking hook is contained:
// the capture it was told about is already safely on disk.
func (s *Server) written(res *capture.Result) {
	s.pendMu.Lock()
	fn := s.afterWrite
	s.pendMu.Unlock()
	if fn == nil || res == nil || s.skipAfterWrite(res.Path) {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error().Interface("panic", r).Str("path", res.Path).Msg("capture after-write hook panicked")
		}
	}()
	fn(res)
}

// CapturePage asks the extension to capture a tab and writes the resulting
// envelope into the inbox, returning where it landed.
func (s *Server) CapturePage(req CaptureRequest) (*capture.Result, error) {
	assembler, writer := s.captureParts()
	if req.Inbox != "" {
		writer = &capture.Writer{Inbox: req.Inbox}
	}
	cmd := req.command()

	ch := make(chan *Response, captureStreamBuffer)
	s.pendMu.Lock()
	if s.streams == nil {
		s.streams = make(map[string]chan *Response)
	}
	s.streams[cmd.ID] = ch
	s.pendMu.Unlock()
	defer func() {
		s.pendMu.Lock()
		delete(s.streams, cmd.ID)
		s.pendMu.Unlock()
		assembler.Drop(cmd.ID)
	}()

	if err := s.writeCommand(req.Target, cmd); err != nil {
		return nil, err
	}
	s.logger.Debug().Str("id", cmd.ID).Str("type", cmd.Type).Msg("command sent")

	deadline := time.NewTimer(req.timeout())
	defer deadline.Stop()
	for {
		select {
		case resp := <-ch:
			if !resp.Success {
				return nil, fmt.Errorf("extension error: %s", resp.Error)
			}
			env, err := assembler.Accept(cmd.ID, resp.dataMap())
			if err != nil {
				return nil, fmt.Errorf("page_capture: %w", err)
			}
			if env == nil {
				continue // more chunks to come
			}
			res, err := writer.Write(env)
			if err == nil {
				s.written(res)
			}
			return res, err
		case <-deadline.C:
			return nil, fmt.Errorf("page_capture timed out after %s", req.timeout())
		}
	}
}

// dispatch routes one decoded response from the extension. A capture is
// streamed (many messages share one id), an ordinary command gets its
// single reply, and a capture nobody asked for is written to the inbox
// anyway — that is the extension flushing the queue it filled while this
// process was down (CLIP-08). Every send here is non-blocking: the read
// loop must never be parked by a slow or vanished receiver, because that
// would stall the whole extension connection.
func (s *Server) dispatch(c *extConn, resp *Response) {
	s.pendMu.Lock()
	stream, streaming := s.streams[resp.ID]
	pending, waiting := s.pending[resp.ID]
	s.pendMu.Unlock()

	switch {
	case streaming:
		select {
		case stream <- resp:
		default:
			s.logger.Error().Str("id", resp.ID).Msg("capture stream buffer full, dropped a message")
		}
	case waiting:
		select {
		case pending <- resp:
		default:
			s.logger.Warn().Str("id", resp.ID).Msg("duplicate response for an already-answered command")
		}
	case resp.Type == keepalivePingType:
		// The extension's own keepalive (background.js, every 20s): it
		// only keeps the socket busy, so there is nothing to route.
	case isCdpEvent(resp):
		s.fanoutCdpEvent(c, resp)
	case isCaptureResponse(resp):
		s.acceptUnsolicitedCapture(resp)
	default:
		s.logger.Warn().Str("id", resp.ID).Msg("no pending request for response")
	}
}
