package main

import (
	"context"
	"testing"
)

func TestOpenURLAllowsOnlyWebAndMailSchemes(t *testing.T) {
	var opened []string
	old := browserOpenURL
	browserOpenURL = func(_ context.Context, u string) { opened = append(opened, u) }
	t.Cleanup(func() { browserOpenURL = old })
	a := newTestApp(t)
	for _, u := range []string{"https://example.com/x", "http://example.com", "mailto:a@b.co"} {
		a.OpenURL(u)
	}
	if len(opened) != 3 {
		t.Fatalf("allowed schemes opened %v, want 3", opened)
	}
	opened = nil
	for _, u := range []string{"javascript:alert(1)", "file:///etc/passwd", "ms-msdt:/id", "myapp://x", "HTTPS-evil:foo", "/etc/passwd", ""} {
		a.OpenURL(u)
	}
	if len(opened) != 0 {
		t.Fatalf("refused schemes were opened: %v", opened)
	}
}
