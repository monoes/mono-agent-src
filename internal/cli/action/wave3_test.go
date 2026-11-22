package action

import (
	"context"
	"strings"
	"testing"
	"time"
)

// `action template capture` hands the URL to the user's logged-in browser, so only plain
// web pages and a bounded --wait may get that far.
func TestTemplateCaptureRefusesBadURLAndWait(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"file", []string{"file:///etc/passwd"}, "unsupported URL scheme"},
		{"javascript", []string{"javascript:alert(1)"}, "unsupported URL scheme"},
		{"chrome", []string{"chrome://settings"}, "unsupported URL scheme"},
		{"no host", []string{"http:///x"}, "no host"},
		{"negative wait", []string{"https://example.com", "--wait", "-1"}, "--wait must be between 0 and 120"},
		{"huge wait", []string{"https://example.com", "--wait", "100000"}, "--wait must be between 0 and 120"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, _ := testDeps(t, false)
			rendered := false
			d.RenderHTML = func(context.Context, string, time.Duration) (string, error) {
				rendered = true
				return "<html></html>", nil
			}
			_, _, err := run(t, d, append([]string{"template", "capture", "--out", t.TempDir() + "/o.html"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if rendered {
				t.Fatal("the browser was asked to open the page")
			}
		})
	}
}
