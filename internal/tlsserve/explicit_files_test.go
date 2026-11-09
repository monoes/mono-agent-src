package tlsserve

import (
	"bytes"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePair writes a fresh certificate and key under dir and returns their paths and the
// certificate's DER bytes, which tell one pair from another.
func writePair(t *testing.T, dir, name string) (certPath, keyPath string, der []byte) {
	t.Helper()
	cert, certPEM, keyPEM, err := GenerateSelfSigned(name)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath = filepath.Join(dir, name+"-cert.pem"), filepath.Join(dir, name+"-key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath, cert.Certificate[0]
}

func noEnvironmentPair(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv(testCertEnv, "")
	t.Setenv(testKeyEnv, "")
	return home
}

// The saved settings of the API name the files: they stand where the environment names none.
func TestResolveUsesTheExplicitFilesWhenTheEnvironmentNamesNone(t *testing.T) {
	home := noEnvironmentPair(t)
	certPath, keyPath, der := writePair(t, t.TempDir(), "saved")

	for _, addr := range []string{"0.0.0.0:0", "127.0.0.1:0"} { // on loopback too: the operator asked for TLS
		c := testConfig(addr)
		c.CertFile, c.KeyFile = certPath, keyPath
		cfg, err := Resolve(c)
		if err != nil || cfg == nil || len(cfg.Certificates) == 0 {
			t.Fatalf("%s: cfg=%v err=%v", addr, cfg, err)
		}
		if !bytes.Equal(cfg.Certificates[0].Certificate[0], der) {
			t.Errorf("%s: the certificate served is not the one in the explicit files", addr)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "tlsserve-test")); err == nil {
		t.Error("a self-signed certificate was made although an explicit pair was given")
	}
}

func TestTheEnvironmentPairWinsOverTheExplicitFiles(t *testing.T) {
	noEnvironmentPair(t)
	dir := t.TempDir()
	envCert, envKey, envDER := writePair(t, dir, "env")
	fileCert, fileKey, _ := writePair(t, dir, "file")
	t.Setenv(testCertEnv, envCert)
	t.Setenv(testKeyEnv, envKey)
	c := testConfig("0.0.0.0:0")
	c.CertFile, c.KeyFile = fileCert, fileKey
	cfg, err := Resolve(c)
	if err != nil || cfg == nil {
		t.Fatalf("cfg=%v err=%v", cfg, err)
	}
	if !bytes.Equal(cfg.Certificates[0].Certificate[0], envDER) {
		t.Error("the explicit files were used although the environment names a pair")
	}
}

// The pair comes from one place: a variable set on its own is an error, and the explicit
// files do not fill in the file it lacks.
func TestOneEnvironmentVariableTakesTheWholePair(t *testing.T) {
	noEnvironmentPair(t)
	dir := t.TempDir()
	envCert, _, _ := writePair(t, dir, "env")
	fileCert, fileKey, _ := writePair(t, dir, "file")
	t.Setenv(testCertEnv, envCert)
	c := testConfig("0.0.0.0:0")
	c.CertFile, c.KeyFile = fileCert, fileKey
	cfg, err := Resolve(c)
	if err == nil {
		t.Fatalf("the certificate variable alone, over a complete explicit pair, gave %v", cfg)
	}
	if !strings.Contains(err.Error(), testCertEnv) || !strings.Contains(err.Error(), "must both be set") {
		t.Errorf("a half pair in the environment is reported as before, naming the variables: %v", err)
	}
}

// Half a pair of explicit files is no certificate, and no reason to fall back to a
// self-signed one on a network bind.
func TestHalfAnExplicitPairIsAnError(t *testing.T) {
	home := noEnvironmentPair(t)
	certPath, keyPath, _ := writePair(t, t.TempDir(), "half")
	for name, c := range map[string]Config{
		"certificate only": {CertFile: certPath},
		"key only":         {KeyFile: keyPath},
	} {
		c.Addr, c.CertEnv, c.KeyEnv = "0.0.0.0:0", testCertEnv, testKeyEnv
		c.CacheDir, c.CommonName, c.Label = "tlsserve-test", "tlsserve test", "test server"
		cfg, err := Resolve(c)
		if err == nil {
			t.Errorf("%s: gave %v", name, cfg)
			continue
		}
		if !strings.Contains(err.Error(), "test server") || strings.Contains(err.Error(), testCertEnv) || !strings.Contains(err.Error(), "must both be given") {
			t.Errorf("%s: the error should name the listener, say both files are needed and not name the variables, which are not what was set: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "tlsserve-test")); err == nil {
		t.Error("a self-signed certificate was made for half a pair")
	}
}

func TestExplicitFilesThatDoNotLoadAreAnErrorThatNamesThePaths(t *testing.T) {
	home := noEnvironmentPair(t)
	c := testConfig("0.0.0.0:0")
	c.CertFile, c.KeyFile = filepath.Join(t.TempDir(), "missing-cert.pem"), filepath.Join(t.TempDir(), "missing-key.pem")
	cfg, err := Resolve(c)
	if err == nil {
		t.Fatalf("gave %v", cfg)
	}
	if !strings.Contains(err.Error(), "missing-cert.pem") || !strings.Contains(err.Error(), "missing-key.pem") {
		t.Errorf("the error should name both files: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "tlsserve-test")); err == nil {
		t.Error("a self-signed certificate was made when the explicit files did not load")
	}
}

// Nothing explicit, nothing in the environment: as before.
func TestResolveWithoutExplicitFilesIsUnchanged(t *testing.T) {
	noEnvironmentPair(t)
	if cfg, err := Resolve(testConfig("127.0.0.1:0")); err != nil || cfg != nil {
		t.Errorf("loopback: cfg=%v err=%v, want plain HTTP", cfg, err)
	}
	if cfg, err := Resolve(testConfig("0.0.0.0:0")); err != nil || cfg == nil {
		t.Errorf("network: cfg=%v err=%v, want a self-signed certificate", cfg, err)
	}
}
