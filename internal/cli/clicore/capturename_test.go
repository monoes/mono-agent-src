package clicore

import (
	"strings"
	"testing"
	"time"
)

func TestCaptureFileName(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	for host, want := range map[string]string{
		"www.example.com": "www_example_com_20260102_030405_123456789.html",
		"::1":             "__1_20260102_030405_123456789.html",
		"EXAMPLE.com":     "example_com_20260102_030405_123456789.html",
	} {
		if got := CaptureFileName(host, at); got != want {
			t.Errorf("CaptureFileName(%q) = %q, want %q", host, got, want)
		}
	}
	if strings.ContainsAny(CaptureFileName("a:b/c\\d", at), ":/\\") {
		t.Error("path or drive characters survive")
	}
}
