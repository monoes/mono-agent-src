package extension

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/capture"
)

// fakeExtension is a WebSocket client standing in for the Chrome extension:
// it authenticates like the real one and lets a test read the commands the
// server sent and write back whatever responses the scenario needs. No
// Chrome, no chrome-extension/ JS.
type fakeExtension struct {
	t    *testing.T
	conn *websocket.Conn
}

// waitBudget bounds a wait for something that must happen: up to shortly
// before the test binary's own timeout, so that a loaded machine slows the
// test down instead of failing it. The wait still ends as soon as the thing
// happens.
func waitBudget(t *testing.T) time.Duration {
	if d, ok := t.Deadline(); ok {
		if left := time.Until(d) - 10*time.Second; left > 5*time.Second {
			return left
		}
		return 5 * time.Second
	}
	return 5 * time.Minute
}

// startCaptureServer brings up a Server on a free port with an isolated
// HOME (so the token never touches real state) and an inbox under the
// test's temp dir, and connects a fake extension to it.
func startCaptureServer(t *testing.T) (*Server, *fakeExtension, string) {
	t.Helper()
	return startCaptureServerWith(t, nil)
}

// startCaptureServerWith is startCaptureServer with a hook that configures
// the server before it starts.
func startCaptureServerWith(t *testing.T, configure func(*Server)) (*Server, *fakeExtension, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())

	port := freePort(t)
	t.Setenv(ExtensionPortEnv, strconv.Itoa(port))

	srv := NewServer("127.0.0.1:9222", zerolog.Nop())
	inbox := filepath.Join(t.TempDir(), "inbox")
	srv.SetCaptureInbox(inbox)
	if configure != nil {
		configure(srv)
	}
	srv.StartAsync(context.Background())
	t.Cleanup(func() { _ = srv.Close() })

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(waitBudget(t))
	for !Probe(base) {
		if time.Now().After(deadline) {
			t.Fatalf("server never became healthy at %s", base)
		}
		time.Sleep(20 * time.Millisecond)
	}

	conn, _, err := websocket.DefaultDialer.Dial("ws://127.0.0.1:"+strconv.Itoa(port)+"/monoagent", nil)
	if err != nil {
		t.Fatalf("dial extension endpoint: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	authenticateTestConn(t, conn)

	if err := srv.WaitForConnection(waitBudget(t)); err != nil {
		t.Fatalf("server never saw the fake extension: %v", err)
	}
	return srv, &fakeExtension{t: t, conn: conn}, inbox
}

// nextCommand reads the next command the server sent, skipping the pings it
// interleaves.
func (f *fakeExtension) nextCommand() *Command {
	f.t.Helper()
	_ = f.conn.SetReadDeadline(time.Now().Add(waitBudget(f.t)))
	_, msg, err := f.conn.ReadMessage()
	if err != nil {
		f.t.Fatalf("read command: %v", err)
	}
	var cmd Command
	if err := json.Unmarshal(msg, &cmd); err != nil {
		f.t.Fatalf("decode command: %v", err)
	}
	return &cmd
}

// answerPings reads the socket in the background for the rest of the test,
// so the server's pings get their pongs the way a real browser's would.
// Without it the server gives up on the socket pongWait after connecting,
// and a test slowed down by a loaded machine loses its connection
// mid-capture. Only for a test that reads nothing more from the socket.
func (f *fakeExtension) answerPings() {
	_ = f.conn.SetReadDeadline(time.Time{})
	go func() {
		for {
			if _, _, err := f.conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
}

func (f *fakeExtension) send(resp any) {
	f.t.Helper()
	blob, err := json.Marshal(resp)
	if err != nil {
		f.t.Fatalf("marshal response: %v", err)
	}
	if err := f.conn.WriteMessage(websocket.TextMessage, blob); err != nil {
		f.t.Fatalf("write response: %v", err)
	}
}

// sendChunk emits one chunk message for id.
func (f *fakeExtension) sendChunk(id, of string, index, total int, body string) {
	f.t.Helper()
	f.send(map[string]any{
		"id":      id,
		"success": true,
		"type":    CmdPageCapture,
		"data": map[string]any{
			"chunk": map[string]any{"index": index, "total": total, "of": of},
			"bytes": base64.StdEncoding.EncodeToString([]byte(body)),
		},
	})
}

// sendFinal emits the closing message for id, exactly as the extension does
// (chrome-extension/capture.js planMessages): always last, always marked
// final, with inline artifacts and chunk receipts side by side.
func (f *fakeExtension) sendFinal(id string, meta map[string]any, artifacts ...map[string]any) {
	f.t.Helper()
	list := make([]any, 0, len(artifacts))
	for _, a := range artifacts {
		list = append(list, a)
	}
	f.send(map[string]any{
		"id":      id,
		"success": true,
		"type":    CmdPageCapture,
		"data": map[string]any{
			"meta":      meta,
			"artifacts": list,
			"warnings":  []any{},
			"final":     true,
		},
	})
}

// chunkReceipt is how the closing message lists an artifact that was
// streamed: the chunk count, and no bytes.
func chunkReceipt(name string, chunks int) map[string]any {
	return map[string]any{"name": name, "encoding": "base64", "chunked": true, "chunks": chunks}
}

func b64Artifact(name, body string) map[string]any {
	return map[string]any{
		"name":     name,
		"encoding": "base64",
		"bytes":    base64.StdEncoding.EncodeToString([]byte(body)),
	}
}

func sampleMeta() map[string]any {
	return map[string]any{
		"url":          "https://example.com/post",
		"canonicalUrl": "https://example.com/post",
		"title":        "A Post",
		"capturedAt":   "2026-09-21T10:11:12Z",
		"httpStatus":   200,
		"tags":         []any{"research"},
		"source":       "extension",
	}
}

func readArtifact(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}

// TestCapturePageWritesEnvelope drives the whole CLIP-01 Go half: command
// out, chunked and inline artifacts back, envelope on disk.
func TestCapturePageWritesEnvelope(t *testing.T) {
	srv, ext, inbox := startCaptureServer(t)

	type outcome struct {
		res *capture.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := srv.CapturePage(CaptureRequest{
			TabID:      7,
			Note:       "read later",
			Tags:       []string{"research"},
			Collection: "inbox",
			Timeout:    5 * time.Second,
		})
		done <- outcome{res, err}
	}()

	cmd := ext.nextCommand()
	if cmd.Type != CmdPageCapture {
		t.Fatalf("command type = %q, want %q", cmd.Type, CmdPageCapture)
	}
	if cmd.TabID != 7 {
		t.Fatalf("tabId = %d, want 7", cmd.TabID)
	}
	if got := cmd.Params["note"]; got != "read later" {
		t.Fatalf("params.note = %v", got)
	}
	if got := cmd.Params["collection"]; got != "inbox" {
		t.Fatalf("params.collection = %v", got)
	}
	if got := stringSlice(cmd.Params["formats"]); len(got) != 4 || got[0] != "mhtml" {
		t.Fatalf("params.formats = %v", got)
	}
	if got, ok := cmd.Params["selection"].(bool); !ok || got {
		t.Fatalf("params.selection = %v", cmd.Params["selection"])
	}

	// The archive is streamed ahead of the envelope, which then lists it as
	// a receipt; everything small enough rides inline.
	ext.sendChunk(cmd.ID, capture.ArtifactMHTML, 0, 2, "first-")
	ext.sendChunk(cmd.ID, capture.ArtifactMHTML, 1, 2, "second")
	ext.sendFinal(cmd.ID, sampleMeta(),
		chunkReceipt(capture.ArtifactMHTML, 2),
		b64Artifact(capture.ArtifactReadable, "# A Post"),
		b64Artifact(capture.ArtifactScreenshot, "\x89PNG"),
	)

	got := <-done
	if got.err != nil {
		t.Fatalf("CapturePage: %v", got.err)
	}
	if filepath.Dir(got.res.Path) != inbox {
		t.Fatalf("envelope landed in %s, want a child of %s", got.res.Path, inbox)
	}
	if body := readArtifact(t, got.res.Path, capture.ArtifactMHTML); body != "first-second" {
		t.Fatalf("page.mhtml = %q", body)
	}
	if body := readArtifact(t, got.res.Path, capture.ArtifactReadable); body != "# A Post" {
		t.Fatalf("readable.md = %q", body)
	}
	if _, err := os.Stat(filepath.Join(got.res.Path, capture.MetaFile)); err != nil {
		t.Fatalf("meta.json: %v", err)
	}
	if got.res.Meta.Title != "A Post" || got.res.Meta.HTTPStatus != 200 {
		t.Fatalf("meta = %+v", got.res.Meta)
	}
	// page.pdf was asked for but not produced — a partial set still lands.
	if _, err := os.Stat(filepath.Join(got.res.Path, capture.ArtifactPDF)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("page.pdf should be absent, got %v", err)
	}
}

func TestCapturePageActiveTabOmitsTabID(t *testing.T) {
	srv, ext, _ := startCaptureServer(t)
	go func() { _, _ = srv.CapturePage(CaptureRequest{Timeout: 2 * time.Second}) }()

	cmd := ext.nextCommand()
	if cmd.TabID != 0 {
		t.Fatalf("tabId = %d, want 0 (meaning the active tab)", cmd.TabID)
	}
	// The zero value must not even be serialized, so the JS side can test
	// for its absence.
	blob, err := json.Marshal(cmd)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(blob, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := round["tabId"]; present {
		t.Fatalf("tabId serialized for an active-tab capture: %s", blob)
	}
}

func TestCapturePageSurfacesExtensionError(t *testing.T) {
	srv, ext, inbox := startCaptureServer(t)

	done := make(chan error, 1)
	go func() {
		_, err := srv.CapturePage(CaptureRequest{Timeout: 5 * time.Second})
		done <- err
	}()
	cmd := ext.nextCommand()
	ext.send(map[string]any{"id": cmd.ID, "success": false, "error": "tab is a chrome:// page"})

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "chrome:// page") {
		t.Fatalf("err = %v, want the extension's message", err)
	}
	if entries, _ := capture.List(inbox); len(entries) != 0 {
		t.Fatalf("a failed capture wrote %v", entries)
	}
}

func TestCapturePageTimesOutOnMissingChunk(t *testing.T) {
	srv, ext, inbox := startCaptureServer(t)

	done := make(chan error, 1)
	go func() {
		_, err := srv.CapturePage(CaptureRequest{Timeout: 300 * time.Millisecond})
		done <- err
	}()
	cmd := ext.nextCommand()
	ext.sendChunk(cmd.ID, capture.ArtifactMHTML, 0, 2, "first-") // and then silence

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if entries, _ := capture.List(inbox); len(entries) != 0 {
		t.Fatalf("a timed-out capture wrote %v", entries)
	}
	assembler, _ := srv.captureParts()
	if assembler.Pending(cmd.ID) {
		t.Fatal("a timed-out capture left its chunks buffered")
	}
}

func TestCapturePageRefusesOversizeCapture(t *testing.T) {
	srv, ext, inbox := startCaptureServer(t)
	srv.SetCaptureOptions(capture.Options{MaxBytes: 1024})

	done := make(chan error, 1)
	go func() {
		_, err := srv.CapturePage(CaptureRequest{Timeout: 5 * time.Second})
		done <- err
	}()
	cmd := ext.nextCommand()
	big := make([]byte, 4096)
	ext.sendFinal(cmd.ID, sampleMeta(), b64Artifact(capture.ArtifactMHTML, string(big)))

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("err = %v, want a size-limit error", err)
	}
	if entries, _ := capture.List(inbox); len(entries) != 0 {
		t.Fatalf("an oversize capture wrote %v", entries)
	}
}

// TestUnsolicitedCaptureIsWritten is CLIP-08's Go half: the extension
// flushes a capture it queued while this process was down, under an id no
// command here is waiting for. It must land in the inbox, not be dropped as
// "no pending request".
func TestUnsolicitedCaptureIsWritten(t *testing.T) {
	srv, ext, inbox := startCaptureServer(t)

	landed := make(chan *capture.Result, 1)
	failed := make(chan error, 1)
	srv.OnCapture(func(res *capture.Result, err error) {
		if err != nil {
			failed <- err
			return
		}
		landed <- res
	})

	// The id shape the extension mints for a push nothing asked for.
	const queuedID = "ext-9f1c2d3e-4a5b-6c7d-8e9f-0a1b2c3d4e5f"
	ext.sendChunk(queuedID, capture.ArtifactMHTML, 0, 2, "saved-")
	ext.sendChunk(queuedID, capture.ArtifactMHTML, 1, 2, "offline")
	ext.sendFinal(queuedID, sampleMeta(),
		chunkReceipt(capture.ArtifactMHTML, 2),
		b64Artifact(capture.ArtifactReadable, "# A Post"),
	)

	select {
	case err := <-failed:
		t.Fatalf("flushed capture failed: %v", err)
	case res := <-landed:
		if filepath.Dir(res.Path) != inbox {
			t.Fatalf("envelope landed in %s, want a child of %s", res.Path, inbox)
		}
		if body := readArtifact(t, res.Path, capture.ArtifactMHTML); body != "saved-offline" {
			t.Fatalf("page.mhtml = %q", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flushed capture never landed")
	}

	entries, err := capture.List(inbox)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Title != "A Post" {
		t.Fatalf("inbox = %+v", entries)
	}
}

// TestUnsolicitedCaptureFailureIsReported: a flushed capture the extension
// could not complete must be surfaced, not silently swallowed.
func TestUnsolicitedCaptureFailureIsReported(t *testing.T) {
	srv, ext, inbox := startCaptureServer(t)

	failed := make(chan error, 1)
	srv.OnCapture(func(_ *capture.Result, err error) {
		if err != nil {
			failed <- err
		}
	})
	ext.send(map[string]any{
		"id":      "queued-2",
		"type":    CmdPageCapture,
		"success": false,
		"error":   "tab was closed before the snapshot finished",
	})

	select {
	case err := <-failed:
		if !strings.Contains(err.Error(), "tab was closed") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a failed flushed capture was swallowed")
	}
	if entries, _ := capture.List(inbox); len(entries) != 0 {
		t.Fatalf("a failed capture wrote %v", entries)
	}
}

// TestUnmatchedNonCaptureResponseIsNotTreatedAsCapture guards the other
// side of that behaviour: a late reply to some ordinary command must not be
// mistaken for a capture and written to the inbox.
func TestUnmatchedNonCaptureResponseIsNotTreatedAsCapture(t *testing.T) {
	srv, ext, inbox := startCaptureServer(t)
	reported := make(chan struct{}, 1)
	srv.OnCapture(func(*capture.Result, error) { reported <- struct{}{} })

	ext.send(Response{ID: "stale-eval", Success: true, Data: map[string]any{"result": "42"}})
	time.Sleep(200 * time.Millisecond)

	select {
	case <-reported:
		t.Fatal("an ordinary response was treated as a capture")
	default:
	}
	if entries, _ := capture.List(inbox); len(entries) != 0 {
		t.Fatalf("inbox = %v", entries)
	}
}

// TestRelayedCaptureIsWrittenByTheOwningProcess covers the second process:
// a short-lived CLI run relays through whoever owns the extension
// connection, and gets back the envelope path rather than the bytes.
func TestRelayedCaptureIsWrittenByTheOwningProcess(t *testing.T) {
	srv, ext, inbox := startCaptureServer(t)
	addr, ok := srv.Addr()
	if !ok {
		t.Fatal("server never bound an address")
	}

	sender := NewRemoteSender("http://" + addr)
	type outcome struct {
		res *capture.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := sender.CapturePage(CaptureRequest{TabID: 3, Timeout: 5 * time.Second})
		done <- outcome{res, err}
	}()

	cmd := ext.nextCommand()
	if cmd.Type != CmdPageCapture || cmd.TabID != 3 {
		t.Fatalf("relayed command = %+v", cmd)
	}
	ext.sendChunk(cmd.ID, capture.ArtifactMHTML, 0, 1, "relayed archive")
	ext.sendFinal(cmd.ID, sampleMeta())

	got := <-done
	if got.err != nil {
		t.Fatalf("relayed CapturePage: %v", got.err)
	}
	if filepath.Dir(got.res.Path) != inbox {
		t.Fatalf("envelope landed in %s, want a child of %s", got.res.Path, inbox)
	}
	if body := readArtifact(t, got.res.Path, capture.ArtifactMHTML); body != "relayed archive" {
		t.Fatalf("page.mhtml = %q", body)
	}
	if got.res.Meta.URL != "https://example.com/post" {
		t.Fatalf("relayed meta = %+v", got.res.Meta)
	}
}

func TestRelayedCaptureSurfacesError(t *testing.T) {
	srv, ext, _ := startCaptureServer(t)
	addr, _ := srv.Addr()
	sender := NewRemoteSender("http://" + addr)

	done := make(chan error, 1)
	go func() {
		_, err := sender.CapturePage(CaptureRequest{Timeout: 5 * time.Second})
		done <- err
	}()
	cmd := ext.nextCommand()
	ext.send(map[string]any{"id": cmd.ID, "success": false, "error": "no active tab"})

	err := <-done
	if err == nil || !strings.Contains(err.Error(), "no active tab") {
		t.Fatalf("err = %v", err)
	}
}

// The extension pings every 20s to keep its service worker's socket alive.
// That ping answers nothing, and logging it as an unmatched response
// filled the bridge's log with a warning three times a minute.
func TestDispatchIgnoresKeepalivePing(t *testing.T) {
	var buf bytes.Buffer
	srv := NewServer("127.0.0.1:0", zerolog.New(&buf))
	srv.dispatch(nil, &Response{Type: "ping"})
	if strings.Contains(buf.String(), "no pending request") {
		t.Fatalf("keepalive ping was logged as unmatched: %s", buf.String())
	}
	srv.dispatch(nil, &Response{ID: "stray", Type: "something_else"})
	if !strings.Contains(buf.String(), "no pending request") {
		t.Fatal("a genuinely unmatched response is no longer reported")
	}
}
