package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/zalando/go-keyring"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var releaseNames = []string{
	"monoagentcli-linux-amd64", "monoagentcli-linux-arm64", "monoagentcli-windows-amd64.exe",
	"monoagentcli-darwin-amd64", "monoagentcli-darwin-arm64",
	"MonoAgent-darwin-arm64.zip", "MonoAgent-windows-amd64.exe", "monoagentcli-windows-amd64-bundled.exe",
	"MonoAgent-linux-amd64.tar.gz", "monoagent-chrome-extension.zip", "NOTICE",
}

func fixture(t *testing.T) string {
	dir := t.TempDir()
	for _, n := range releaseNames {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("bytes of "+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestManifestGeneration(t *testing.T) {
	dir := fixture(t)
	if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	var m Manifest
	raw, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Schema != 1 || m.Version != "v1.2.3" || len(m.Assets) != 10 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if _, err := time.Parse(time.RFC3339, m.ReleasedAt); err != nil {
		t.Fatal(err)
	}
	got := map[string]Asset{}
	for _, a := range m.Assets {
		got[a.Name] = a
	}
	if got["NOTICE"].Name != "" {
		t.Fatal("NOTICE is not a download and must not be an asset")
	}
	ext := got["monoagent-chrome-extension.zip"]
	if ext.Kind != "extension" || ext.OS != "" || ext.Arch != "" || len(ext.SHA256) != 64 ||
		ext.URL != "https://github.com/monoes/mono-agent/releases/download/v1.2.3/monoagent-chrome-extension.zip" {
		t.Errorf("extension asset: %+v", ext)
	}
	// The extension has no platform: os and arch are left out of its entry, so
	// a client picking by os/arch never selects it.
	var rawAssets struct {
		Assets []map[string]any `json:"assets"`
	}
	if err := json.Unmarshal(raw, &rawAssets); err != nil {
		t.Fatal(err)
	}
	for _, a := range rawAssets.Assets {
		_, hasOS := a["os"]
		_, hasArch := a["arch"]
		if (a["kind"] == "extension") == (hasOS || hasArch) {
			t.Errorf("os/arch presence wrong for %v", a)
		}
	}
	cases := map[string][3]string{
		"monoagentcli-windows-amd64.exe": {"windows", "amd64", "cli"},
		"monoagentcli-darwin-arm64":      {"darwin", "arm64", "cli"},
		"MonoAgent-darwin-arm64.zip":     {"darwin", "arm64", "app"},
		"MonoAgent-linux-amd64.tar.gz":   {"linux", "amd64", "app"},
		"MonoAgent-windows-amd64.exe":    {"windows", "amd64", "app"},
	}
	for n, w := range cases {
		a := got[n]
		if a.OS != w[0] || a.Arch != w[1] || a.Kind != w[2] {
			t.Errorf("%s classified %+v, want %v", n, a, w)
		}
	}
	a := got["monoagentcli-linux-amd64"]
	if a.Size != int64(len("bytes of monoagentcli-linux-amd64")) || len(a.SHA256) != 64 ||
		a.URL != "https://github.com/monoes/mono-agent/releases/download/v1.2.3/monoagentcli-linux-amd64" {
		t.Errorf("bad asset: %+v", a)
	}
	sums, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if !strings.Contains(string(sums), "  NOTICE\n") {
		t.Error("NOTICE is not an asset but must still be checksummed in SHA256SUMS")
	}
	if !strings.Contains(string(sums), a.SHA256+"  monoagentcli-linux-amd64\n") || strings.Count(string(sums), "\n") != len(releaseNames) {
		t.Errorf("SHA256SUMS wrong:\n%s", sums)
	}
	// Re-running must not fold its own outputs into the manifest.
	if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	if string(again) != string(sums) {
		t.Error("second run changed SHA256SUMS")
	}
}

func TestManifestRejectsBadInput(t *testing.T) {
	if err := run([]string{"manifest", "-dir", fixture(t), "-version", "1.2"}); err == nil {
		t.Error("bad version accepted")
	}
	if err := run([]string{"manifest", "-dir", t.TempDir(), "-version", "v1.0.0"}); err == nil {
		t.Error("empty dir accepted")
	}
}

func TestSignThenVerify(t *testing.T) {
	dir := fixture(t)
	if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "test.key")
	if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mpath := filepath.Join(dir, "manifest.json")
	if err := run([]string{"sign", "-manifest", mpath, "-key-file", keyFile}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(mpath)
	sig, _ := os.ReadFile(mpath + ".sig")
	if err := verifySignature(data, string(sig), []ed25519.PublicKey{pub}); err != nil {
		t.Fatal(err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if verifySignature(data, string(sig), []ed25519.PublicKey{other}) == nil {
		t.Error("verified against the wrong key")
	}
	if verifySignature(append(data, ' '), string(sig), []ed25519.PublicKey{pub}) == nil {
		t.Error("verified a tampered manifest")
	}
}

func TestSignArgsAndKeys(t *testing.T) {
	if err := run([]string{"sign", "-manifest", "x"}); err == nil {
		t.Error("sign without a key source accepted")
	}
	if _, err := parsePrivateKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Error("short key accepted")
	}
}

func TestExpiresDays(t *testing.T) {
	dir := fixture(t)
	read := func() Manifest {
		var m Manifest
		raw, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "expires_at") != (m.ExpiresAt != "") {
			t.Fatal("expires_at presence mismatch")
		}
		return m
	}
	if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	if m := read(); m.ExpiresAt != "" {
		t.Errorf("default must omit expires_at, got %q", m.ExpiresAt)
	}
	if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3", "-expires-days", "30"}); err != nil {
		t.Fatal(err)
	}
	exp, err := time.Parse(time.RFC3339, read().ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(exp); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("expires_at %v not ~30 days out", exp)
	}
	if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3", "-expires-days", "-1"}); err == nil {
		t.Error("negative -expires-days accepted")
	}
}

func TestSignFromKeyringSeedAndFullKey(t *testing.T) {
	for name, mk := range map[string]func(ed25519.PrivateKey) []byte{
		"full key": func(p ed25519.PrivateKey) []byte { return p },
		"seed":     func(p ed25519.PrivateKey) []byte { return p.Seed() },
	} {
		t.Run(name, func(t *testing.T) {
			keyring.MockInit()
			pub, priv, _ := ed25519.GenerateKey(rand.Reader)
			if err := keyring.Set(keyringService, keyringAccount, base64.StdEncoding.EncodeToString(mk(priv))); err != nil {
				t.Fatal(err)
			}
			dir := fixture(t)
			if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3"}); err != nil {
				t.Fatal(err)
			}
			mpath := filepath.Join(dir, "manifest.json")
			if err := run([]string{"sign", "-manifest", mpath, "-keyring"}); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(mpath)
			sig, _ := os.ReadFile(mpath + ".sig")
			if strings.Count(string(sig), "\n") != 1 || len(strings.Fields(string(sig))) != 2 {
				t.Errorf("sig must be one line, got %q", sig)
			}
			if err := verifySignature(data, string(sig), []ed25519.PublicKey{pub}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckRequiresTheExtensionInTheManifest(t *testing.T) {
	dir := fixture(t)
	if err := run([]string{"manifest", "-dir", dir, "-version", "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	mpath := filepath.Join(dir, "manifest.json")
	prefix := "https://github.com/monoes/mono-agent/releases/download/v1.2.3/"
	args := []string{"check", "-manifest", mpath, "-assets-dir", dir, "-expect-version", "v1.2.3", "-url-prefix", prefix}
	if err := run(args); err != nil {
		t.Fatalf("check of a fresh manifest: %v", err)
	}
	// A manifest written before the extension was an asset no longer matches
	// a dir that holds the zip.
	var m Manifest
	raw, _ := os.ReadFile(mpath)
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var kept []Asset
	for _, a := range m.Assets {
		if a.Kind != "extension" {
			kept = append(kept, a)
		}
	}
	m.Assets = kept
	out, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(mpath, out, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(args); err == nil || !strings.Contains(err.Error(), "monoagent-chrome-extension.zip is missing from the manifest") {
		t.Errorf("check without the extension entry: %v", err)
	}
	// The extension entry must not claim a platform.
	m.Assets = append(kept, Asset{OS: "linux", Arch: "amd64", Name: "monoagent-chrome-extension.zip", URL: prefix + "monoagent-chrome-extension.zip", Kind: "extension"})
	out, _ = json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(mpath, out, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(args); err == nil || !strings.Contains(err.Error(), "os/arch/kind do not match") {
		t.Errorf("check with a platform on the extension: %v", err)
	}
}
