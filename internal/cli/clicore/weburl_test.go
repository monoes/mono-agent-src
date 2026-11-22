package clicore

import (
	"strings"
	"testing"
)

func TestWebURL(t *testing.T) {
	if u, err := WebURL("https://example.com/a"); err != nil || u.Hostname() != "example.com" {
		t.Fatalf("good url: %v %v", u, err)
	}
	for in, want := range map[string]string{
		"file:///etc/passwd": "unsupported URL scheme",
		"javascript:1":       "unsupported URL scheme",
		"http:///x":          "no host",
		"https://":           "no host",
		"::bad":              "invalid URL",
	} {
		if _, err := WebURL(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("WebURL(%q) = %v, want %q", in, err, want)
		}
	}
}
