package mcp

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// A server started with mutations on says so on stderr (never stdout, which
// is the JSON-RPC stream); a read-only one is silent (r28-R28-5).
func TestServeLogsWhenMutationsAreEnabled(t *testing.T) {
	for _, allow := range []bool{true, false} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		orig := os.Stderr
		os.Stderr = w
		var out strings.Builder
		_ = NewServer(Options{AllowMutations: allow}).Serve(context.Background(), strings.NewReader(""), &out)
		os.Stderr = orig
		w.Close()
		b, _ := io.ReadAll(r)
		r.Close()
		if got := strings.Contains(string(b), "mutating tools are ENABLED"); got != allow {
			t.Errorf("allow=%v: stderr %q", allow, b)
		}
		if strings.Contains(out.String(), "ENABLED") {
			t.Errorf("the notice leaked into the JSON-RPC stream: %q", out.String())
		}
	}
}
