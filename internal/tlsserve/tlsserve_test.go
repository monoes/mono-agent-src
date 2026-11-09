package tlsserve

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	testCertEnv = "TLSSERVE_TEST_CERT"
	testKeyEnv  = "TLSSERVE_TEST_KEY"
)

func testConfig(addr string) Config {
	return Config{
		Addr: addr, CertEnv: testCertEnv, KeyEnv: testKeyEnv,
		CacheDir: "tlsserve-test", CommonName: "tlsserve test (self-signed)", Label: "test server",
	}
}

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:9322", true},
		{"localhost:9322", true},
		{"[::1]:9322", true},
		{"0.0.0.0:9322", false},
		{"192.168.1.5:9322", false},
		{":9322", false}, // no host binds every interface
		{"example.com:9322", false},
	}
	for _, c := range cases {
		if got := IsLoopbackAddr(c.addr); got != c.want {
			t.Errorf("IsLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestResolveLoopbackStaysPlain(t *testing.T) {
	t.Setenv(testCertEnv, "")
	t.Setenv(testKeyEnv, "")
	cfg, err := Resolve(testConfig("127.0.0.1:0"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Fatal("a loopback bind with no certificate configured must stay plain HTTP")
	}
}

func TestResolveNonLoopbackUsesCachedSelfSigned(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(testCertEnv, "")
	t.Setenv(testKeyEnv, "")

	var warned string
	c := testConfig("0.0.0.0:0")
	c.Warn = func(msg string) { warned = msg }

	cfg, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.Certificates) == 0 {
		t.Fatal("a non-loopback bind must get a certificate, never plain HTTP")
	}
	if !strings.Contains(warned, "self-signed") || !strings.Contains(warned, testCertEnv) {
		t.Errorf("warning %q should say a self-signed certificate is in use and name %s", warned, testCertEnv)
	}

	keyPath := filepath.Join(home, ".monoagent", "tlsserve-test", "key.pem")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("expected the key cached at %s: %v", keyPath, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %o, want 600", perm)
	}

	again, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.Certificates[0].Certificate[0]) != string(again.Certificates[0].Certificate[0]) {
		t.Fatal("the cached certificate was regenerated instead of reused")
	}
}

func TestResolveExplicitCertAndKey(t *testing.T) {
	_, certPEM, keyPEM, err := GenerateSelfSigned("explicit")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv(testCertEnv, certPath)
	t.Setenv(testKeyEnv, keyPath)
	cfg, err := Resolve(testConfig("0.0.0.0:0"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil || len(cfg.Certificates) == 0 {
		t.Fatal("the explicit certificate was not loaded")
	}

	// An explicit pair also applies on loopback: the operator asked for TLS.
	cfg, err = Resolve(testConfig("127.0.0.1:0"))
	if err != nil || cfg == nil {
		t.Fatalf("loopback with an explicit pair: cfg=%v err=%v", cfg, err)
	}

	// One of the pair alone is an error, never a silent fallback.
	t.Setenv(testKeyEnv, "")
	if _, err := Resolve(testConfig("0.0.0.0:0")); err == nil {
		t.Fatal("expected an error when only the certificate variable is set")
	}
}

func TestResolveTightensTheModesOfACachedKeyAndItsFolder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(testCertEnv, "")
	t.Setenv(testKeyEnv, "")
	c := testConfig("0.0.0.0:0")
	if _, err := Resolve(c); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(home, ".monoagent", "tlsserve-test")
	if err := os.Chmod(filepath.Join(dir, "key.pem"), 0o644); err != nil { // as a restored backup might leave it
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(c); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{filepath.Join(dir, "key.pem"): 0o600, dir: 0o700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Errorf("%s mode = %v (err %v), want %o", path, info.Mode().Perm(), err, want)
		}
	}
}
