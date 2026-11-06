//go:build e2ehost

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The e2e host's binding endpoint answers only the page it serves: its own Host, no foreign Origin, and a JSON content type.
func TestE2EHostRefusesCallsNotFromItsOwnPage(t *testing.T) {
	const addr = "127.0.0.1:34999"
	for name, c := range map[string]struct {
		host, origin, ctype string
		want                int
	}{
		"its own page":   {addr, "http://" + addr, "application/json", http.StatusOK},
		"no origin":      {addr, "", "application/json", http.StatusOK},
		"foreign origin": {addr, "http://evil.example", "application/json", http.StatusForbidden},
		"foreign host":   {"localhost:34999", "", "application/json", http.StatusForbidden},
		"form post":      {addr, "", "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		"text/plain":     {addr, "", "text/plain", http.StatusUnsupportedMediaType},
	} {
		req := httptest.NewRequest(http.MethodPost, "/__call/IsReady", strings.NewReader("[]"))
		req.Host = c.host
		req.Header.Set("Content-Type", c.ctype)
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		e2eCall(NewApp(), rec, req, addr)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", name, rec.Code, c.want, rec.Body.String())
		}
	}
}
