package workflow

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/monoes/mono-agent/internal/testhome"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:9321", true},
		{"localhost:9321", true},
		{"[::1]:9321", true},
		{"0.0.0.0:9321", false},
		{"192.168.1.5:9321", false},
		{":9321", false}, // no host = all interfaces, not loopback
	}
	for _, c := range cases {
		if got := isLoopbackAddr(c.addr); got != c.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

// TestResolveTLSConfig_LoopbackStaysPlain verifies a loopback bind with no
// TLS env vars set gets no TLS config at all — matching the "loopback =
// same-user trust" pattern used elsewhere (e.g. the Chrome extension
// bridge).
func TestResolveTLSConfig_LoopbackStaysPlain(t *testing.T) {
	t.Setenv(webhookTLSCertEnv, "")
	t.Setenv(webhookTLSKeyEnv, "")
	s := NewWebhookServer("127.0.0.1:0", zerolog.Nop())
	cfg, err := s.resolveTLSConfig()
	if err != nil {
		t.Fatalf("resolveTLSConfig: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil TLS config for a loopback bind, got non-nil")
	}
}

// TestResolveTLSConfig_NonLoopbackAutoGeneratesSelfSigned verifies that a
// non-loopback bind with no explicit cert/key configured gets an
// auto-generated self-signed certificate rather than falling through to
// plaintext, and that the certificate is cached to disk under
// ~/.monoagent/webhook-tls/ (mode 0600 for the key).
func TestResolveTLSConfig_NonLoopbackAutoGeneratesSelfSigned(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(webhookTLSCertEnv, "")
	t.Setenv(webhookTLSKeyEnv, "")

	s := NewWebhookServer("0.0.0.0:0", zerolog.Nop())
	cfg, err := s.resolveTLSConfig()
	if err != nil {
		t.Fatalf("resolveTLSConfig: %v", err)
	}
	if cfg == nil || len(cfg.Certificates) == 0 {
		t.Fatal("expected an auto-generated self-signed certificate for a non-loopback bind")
	}

	keyPath := filepath.Join(home, ".monoagent", "webhook-tls", "key.pem")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("expected cached key file at %s: %v", keyPath, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("key file perms = %o, want 600", perm)
	}

	// A second resolve must reuse the cached cert, not regenerate it.
	cfg2, err := s.resolveTLSConfig()
	if err != nil {
		t.Fatalf("resolveTLSConfig (second call): %v", err)
	}
	if len(cfg.Certificates[0].Certificate) == 0 || len(cfg2.Certificates[0].Certificate) == 0 {
		t.Fatal("expected non-empty certificate chains")
	}
	if string(cfg.Certificates[0].Certificate[0]) != string(cfg2.Certificates[0].Certificate[0]) {
		t.Fatal("self-signed certificate was regenerated instead of reused from cache")
	}
}

// TestResolveTLSConfig_ExplicitCertKey verifies MONOAGENT_WEBHOOK_TLS_CERT/
// MONOAGENT_WEBHOOK_TLS_KEY are honored when both are set, and that setting
// only one of the pair is rejected outright rather than silently ignored.
func TestResolveTLSConfig_ExplicitCertKey(t *testing.T) {
	dir := t.TempDir()
	_, certPEM, keyPEM, err := generateSelfSignedCert()
	if err != nil {
		t.Fatalf("generateSelfSignedCert: %v", err)
	}
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	t.Setenv(webhookTLSCertEnv, certPath)
	t.Setenv(webhookTLSKeyEnv, keyPath)
	s := NewWebhookServer("0.0.0.0:0", zerolog.Nop())
	cfg, err := s.resolveTLSConfig()
	if err != nil {
		t.Fatalf("resolveTLSConfig with explicit cert/key: %v", err)
	}
	if cfg == nil || len(cfg.Certificates) == 0 {
		t.Fatal("expected the explicit certificate to be loaded")
	}

	// Only one of the pair set → reject rather than silently falling back.
	t.Setenv(webhookTLSKeyEnv, "")
	s2 := NewWebhookServer("0.0.0.0:0", zerolog.Nop())
	if _, err := s2.resolveTLSConfig(); err == nil {
		t.Fatal("expected an error when only MONOAGENT_WEBHOOK_TLS_CERT is set")
	}
}

// TestWebhookServer_TLSEndToEnd starts the webhook server bound to a
// non-loopback address (forcing the self-signed auto-TLS path) and verifies
// a real HTTPS request round-trips successfully.
func TestWebhookServer_TLSEndToEnd(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(webhookTLSCertEnv, "")
	t.Setenv(webhookTLSKeyEnv, "")

	s := NewWebhookServer("0.0.0.0:0", zerolog.Nop())
	fired := false
	if err := s.Register(&WebhookRegistration{
		Path:      "tls-hook",
		Method:    "POST",
		TriggerFn: func(items []Item) { fired = true },
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Start manually (rather than via s.Start, whose net.Listen("tcp",
	// "0.0.0.0:0") host part we need for the client URL) so we can read
	// back the actual bound port.
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tlsConfig, err := s.resolveTLSConfig()
	if err != nil {
		t.Fatalf("resolveTLSConfig: %v", err)
	}
	if tlsConfig == nil {
		t.Fatal("expected a TLS config for a non-loopback bind")
	}
	tlsLn := tls.NewListener(ln, tlsConfig)
	go s.server.Serve(tlsLn)
	defer s.server.Close()

	port := ln.Addr().(*net.TCPAddr).Port

	certPool := x509.NewCertPool()
	leaf, err := x509.ParseCertificate(tlsConfig.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("parsing generated leaf cert: %v", err)
	}
	certPool.AddCert(leaf)
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: certPool},
		},
		Timeout: 5 * time.Second,
	}

	url := fmt.Sprintf("https://127.0.0.1:%d/webhook/tls-hook", port)
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		t.Fatalf("https POST: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var result map[string]bool
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !result["success"] {
		t.Fatalf("unexpected response body: %s", body)
	}
	if !fired {
		t.Fatal("expected the trigger to fire over TLS")
	}
}
