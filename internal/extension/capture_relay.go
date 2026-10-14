package extension

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"time"

	"github.com/monoes/mono-agent/internal/capture"
)

// keepalivePingType is the type of the application-level ping the
// extension sends every 20s to keep its service worker's socket alive.
const keepalivePingType = "ping"

// isCaptureResponse reports whether a response that matches no pending
// command is a page_capture message. The type field settles it outright;
// the shape check is the fallback for a sender that omits it.
func isCaptureResponse(resp *Response) bool {
	if resp.Type == CmdPageCapture {
		return true
	}
	data := resp.dataMap()
	if data == nil {
		return false
	}
	if _, chunked := data["chunk"]; chunked {
		return true
	}
	_, hasMeta := data["meta"]
	_, hasArtifacts := data["artifacts"]
	return hasMeta && hasArtifacts
}

// acceptUnsolicitedCapture feeds a flushed-queue capture through the same
// assembler and writer as a requested one. Called from the read loop, so
// the disk write is handed to a goroutine — a 60MB envelope must not hold
// up the extension connection.
func (s *Server) acceptUnsolicitedCapture(resp *Response) {
	assembler, writer := s.captureParts()
	if !resp.Success {
		s.logger.Warn().Str("id", resp.ID).Str("error", resp.Error).Msg("queued capture failed in the extension")
		s.reportCapture(nil, fmt.Errorf("extension error: %s", resp.Error))
		return
	}
	env, err := assembler.Accept(resp.ID, resp.dataMap())
	if err != nil {
		s.logger.Error().Err(err).Str("id", resp.ID).Msg("queued capture could not be assembled")
		s.reportCapture(nil, err)
		return
	}
	if env == nil {
		return // more chunks to come
	}
	go func() {
		res, err := writer.Write(env)
		if err != nil {
			s.logger.Error().Err(err).Str("id", resp.ID).Msg("queued capture could not be written")
		} else {
			s.logger.Info().Str("path", res.Path).Msg("wrote a capture the extension had queued")
			s.written(res)
		}
		s.reportCapture(res, err)
	}()
}

// reportCapture invokes the OnCapture callback, if one is registered.
func (s *Server) reportCapture(res *capture.Result, err error) {
	s.pendMu.Lock()
	fn := s.onCapture
	s.pendMu.Unlock()
	if fn != nil {
		fn(res, err)
	}
}

// sweepCaptures drops captures whose remaining chunks never arrived. Driven
// by the ping loop: a requested capture has its own deadline, but a flushed
// one has nobody waiting on it, so without this its buffered chunks would
// be held until the process exits.
func (s *Server) sweepCaptures() {
	s.pendMu.Lock()
	assembler := s.assembler
	s.pendMu.Unlock()
	if assembler == nil {
		return // nothing has captured yet
	}
	for _, id := range assembler.Expire(time.Now()) {
		s.logger.Warn().Str("id", id).Msg("dropped a capture whose chunks stopped arriving")
		s.reportCapture(nil, capture.ErrExpired)
	}
}

// serveRelayCapture answers a relayed page_capture (see handleRelay). The
// process that owns the extension connection is the one that assembles and
// writes the envelope; the caller gets back only the small Result, so a
// 60MB archive never crosses the loopback HTTP hop.
func (s *Server) serveRelayCapture(w http.ResponseWriter, target Target, cmd *Command, timeout time.Duration, inbox string) {
	req := captureRequestFromCommand(cmd, timeout, inbox)
	req.Target = target
	res, err := s.CapturePage(req)
	resp := &Response{ID: cmd.ID, Type: CmdPageCapture}
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Success = true
		resp.Data = res
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// ---------------------------------------------------------------------------
// Relay side
// ---------------------------------------------------------------------------

// CapturePage relays a capture through the process that owns the extension
// connection. That process writes the envelope; this one only learns where
// it landed.
func (r *RemoteSender) CapturePage(req CaptureRequest) (*capture.Result, error) {
	cmd := req.command()
	body, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("marshal command: %w", err)
	}
	timeout := req.timeout()
	url := fmt.Sprintf("%s/monoagent/relay?timeout_ms=%d", r.baseURL, timeout.Milliseconds())
	if req.Inbox != "" {
		url += "&inbox=" + neturl.QueryEscape(req.Inbox)
	}
	if enc := targetValues(req.Target).Encode(); enc != "" {
		url += "&" + enc
	}
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set(tokenHeader, r.token)

	// A capture can legitimately outlast the shared client's fixed
	// timeout, so this one request gets a client sized to the capture.
	client := &http.Client{Timeout: timeout + 30*time.Second}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("relay request: %w", err)
	}
	defer httpResp.Body.Close()

	resp, err := decodeRelayResponse(httpResp)
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		if resp.Error == "" {
			resp.Error = httpResp.Status
		}
		return nil, fmt.Errorf("page_capture: %s", resp.Error)
	}
	blob, err := json.Marshal(resp.Data)
	if err != nil {
		return nil, fmt.Errorf("re-encode capture result: %w", err)
	}
	var res capture.Result
	if err := json.Unmarshal(blob, &res); err != nil {
		return nil, fmt.Errorf("decode capture result: %w", err)
	}
	if res.Path == "" {
		return nil, fmt.Errorf("relayed capture returned no envelope path")
	}
	return &res, nil
}
