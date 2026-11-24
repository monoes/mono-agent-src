package httpapi

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// A server that serves mutating endpoints says so at start, and a read-only
// one does not (r28-R28-5).
func TestServeLogsWhenMutationsAreEnabled(t *testing.T) {
	for _, allow := range []bool{true, false} {
		s := newTestServer(t, allow)
		var buf bytes.Buffer
		s.warnOut = &buf
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { _ = s.Serve(ctx, ln); close(done) }()
		time.Sleep(200 * time.Millisecond)
		cancel()
		<-done
		if got := strings.Contains(buf.String(), "mutating endpoints are ENABLED"); got != allow {
			t.Errorf("allow=%v: startup output %q", allow, buf.String())
		}
	}
}
