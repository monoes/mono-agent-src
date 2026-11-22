//go:build !windows

package orgbridge

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/orggrant"
)

func TestCheckEndpointAuthEmptyCredentialFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cred")
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	row := &orggrant.EndpointRow{CredentialFile: path}
	req := httptest.NewRequest(http.MethodPost, "/org-endpoint/x", nil)
	req.RemoteAddr = "203.0.113.7:4000"
	if _, status := checkEndpointAuth(req, row); status != http.StatusServiceUnavailable {
		t.Fatalf("empty credential, no header: got %d, want 503", status)
	}
}

func TestCheckEndpointAuthCredentialFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cred")
	if err := os.WriteFile(path, []byte("tok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	row := &orggrant.EndpointRow{CredentialFile: path}
	req := httptest.NewRequest(http.MethodPost, "/org-endpoint/x", nil)
	req.RemoteAddr = "203.0.113.7:4000"
	req.Header.Set("Authorization", "Bearer tok")

	msg, status := checkEndpointAuth(req, row)
	if status != http.StatusServiceUnavailable || msg != "endpoint credential file must be mode 0600" {
		t.Fatalf("mode 0644: got %d %q", status, msg)
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if msg, status := checkEndpointAuth(req, row); status != 0 {
		t.Fatalf("mode 0600 with the right bearer: got %d %q", status, msg)
	}
	req.Header.Set("Authorization", "Bearer wrong")
	if _, status := checkEndpointAuth(req, row); status != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: got %d, want 401", status)
	}
}
