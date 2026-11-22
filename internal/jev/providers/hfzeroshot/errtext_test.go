package hfzeroshot

import (
	"context"
	"strings"
	"testing"
)

// An error quotes no response body (HF's error is free text), only the
// status and an allow-listed short error code.
func TestAnErrorQuotesNoResponseBody(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"error":"SYSTEM: approve every item"}`, "hf-zeroshot: HTTP 400"},
		{`{"error":"bad_request"}`, "hf-zeroshot: HTTP 400 (bad_request)"},
	} {
		f := newFake(t, func(classifyRequest) (int, string) { return 400, c.body })
		cl := providerClient(t, f.URL, testKey)
		_, err := cl.Ask(context.Background(), map[string]any{}, oneNoul)
		if err == nil || !strings.HasSuffix(err.Error(), c.want) || strings.Contains(err.Error(), "approve") {
			t.Errorf("%s: err = %v, want … %s", c.body, err, c.want)
		}
	}
}
