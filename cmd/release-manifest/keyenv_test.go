package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newKeyEnv(t *testing.T, raw []byte) {
	t.Helper()
	t.Setenv("TEST_RELEASE_KEY", base64.StdEncoding.EncodeToString(raw))
}

const testRepo = "monoes/mono-agent"

func prefixFor(version string) string {
	return "https://github.com/" + testRepo + "/releases/download/" + version + "/"
}

func manifestFixture(t *testing.T, version string) string {
	t.Helper()
	dir := fixture(t)
	if err := run([]string{"manifest", "-dir", dir, "-version", version}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSignKeyEnvRoundTrip(t *testing.T) {
	for name, mk := range map[string]func(ed25519.PrivateKey) []byte{
		"full key": func(p ed25519.PrivateKey) []byte { return p },
		"seed":     func(p ed25519.PrivateKey) []byte { return p.Seed() },
	} {
		t.Run(name, func(t *testing.T) {
			pub, priv, _ := ed25519.GenerateKey(rand.Reader)
			newKeyEnv(t, mk(priv))
			dir := manifestFixture(t, "v1.2.3")
			mpath := filepath.Join(dir, "manifest.json")
			pubOut := filepath.Join(t.TempDir(), "pub.txt")
			err := run([]string{"sign", "-manifest", mpath, "-key-env", "TEST_RELEASE_KEY",
				"-assets-dir", dir, "-expect-version", "v1.2.3", "-url-prefix", prefixFor("v1.2.3"), "-min-version", "v1.2.2", "-pubkey-out", pubOut})
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(mpath)
			sig, _ := os.ReadFile(mpath + ".sig")
			if err := verifySignature(data, string(sig), []ed25519.PublicKey{pub}); err != nil {
				t.Fatal(err)
			}
			line, _ := os.ReadFile(pubOut)
			if want := keyID(pub) + " " + base64.StdEncoding.EncodeToString(pub) + "\n"; string(line) != want {
				t.Errorf("pubkey-out %q, want %q", line, want)
			}
			if strings.Contains(string(line), base64.StdEncoding.EncodeToString(priv)) {
				t.Error("private key leaked into pubkey-out")
			}
		})
	}
}

func TestSignKeyEnvRefusals(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := manifestFixture(t, "v1.2.3")
	mpath := filepath.Join(dir, "manifest.json")
	base := []string{"sign", "-manifest", mpath, "-key-env", "TEST_RELEASE_KEY"}
	with := func(extra ...string) []string { return append(append([]string{}, base...), extra...) }

	t.Setenv("TEST_RELEASE_KEY", "")
	if err := run(with("-assets-dir", dir, "-expect-version", "v1.2.3", "-url-prefix", prefixFor("v1.2.3"))); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty variable accepted: %v", err)
	}
	newKeyEnv(t, priv)
	if err := run(base); err == nil {
		t.Error("-key-env without -assets-dir/-expect-version accepted")
	}
	if err := run(with("-assets-dir", dir, "-expect-version", "v1.2.3")); err == nil || !strings.Contains(err.Error(), "url-prefix") {
		t.Errorf("-key-env without -url-prefix accepted: %v", err)
	}
	if err := run(with("-assets-dir", dir, "-expect-version", "v1.2.3", "-url-prefix", prefixFor("v1.2.3"), "-keyring")); err == nil {
		t.Error("two key sources accepted")
	}
	// An error must never echo the key.
	t.Setenv("TEST_RELEASE_KEY", "not-base64-!!!secretsecret")
	if err := run(with("-assets-dir", dir, "-expect-version", "v1.2.3", "-url-prefix", prefixFor("v1.2.3"))); err == nil || strings.Contains(err.Error(), "secretsecret") {
		t.Errorf("bad key error missing or leaks: %v", err)
	}
	if _, err := os.Stat(mpath + ".sig"); err == nil {
		t.Error("a signature was written despite refusals")
	}
}

func TestSignRefusesBadManifest(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	sign := func(dir, ver, min string) error {
		newKeyEnv(t, priv) // sign unsets the variable once it has read it
		args := []string{"sign", "-manifest", filepath.Join(dir, "manifest.json"), "-key-env", "TEST_RELEASE_KEY",
			"-assets-dir", dir, "-expect-version", ver, "-url-prefix", prefixFor(ver)}
		if min != "" {
			args = append(args, "-min-version", min)
		}
		return run(args)
	}
	t.Run("checksum mismatch", func(t *testing.T) {
		dir := manifestFixture(t, "v1.2.3")
		os.WriteFile(filepath.Join(dir, "monoagentcli-linux-amd64"), []byte("tampered after manifest"), 0o644)
		if err := sign(dir, "v1.2.3", ""); err == nil || !strings.Contains(err.Error(), "sha256") {
			t.Errorf("tampered asset signed: %v", err)
		}
	})
	t.Run("size mismatch", func(t *testing.T) {
		dir := manifestFixture(t, "v1.2.3")
		mpath := filepath.Join(dir, "manifest.json")
		raw, _ := os.ReadFile(mpath)
		var m Manifest
		json.Unmarshal(raw, &m)
		m.Assets[0].Size++
		out, _ := json.Marshal(m)
		os.WriteFile(mpath, out, 0o644)
		if err := sign(dir, "v1.2.3", ""); err == nil || !strings.Contains(err.Error(), "size") {
			t.Errorf("wrong size signed: %v", err)
		}
	})
	t.Run("missing asset file", func(t *testing.T) {
		dir := manifestFixture(t, "v1.2.3")
		os.Remove(filepath.Join(dir, "MonoAgent-linux-amd64.tar.gz"))
		if err := sign(dir, "v1.2.3", ""); err == nil {
			t.Error("manifest with a missing asset signed")
		}
	})
	t.Run("built file not in manifest", func(t *testing.T) {
		dir := manifestFixture(t, "v1.2.3")
		os.WriteFile(filepath.Join(dir, "MonoAgent-darwin-amd64.zip"), []byte("x"), 0o644)
		if err := sign(dir, "v1.2.3", ""); err == nil || !strings.Contains(err.Error(), "missing from the manifest") {
			t.Errorf("unlisted platform file signed: %v", err)
		}
	})
	t.Run("wrong version", func(t *testing.T) {
		dir := manifestFixture(t, "v1.2.3")
		if err := sign(dir, "v1.2.4", ""); err == nil || !strings.Contains(err.Error(), "being released") {
			t.Errorf("version not equal to the release signed: %v", err)
		}
	})
	t.Run("lower than latest published", func(t *testing.T) {
		dir := manifestFixture(t, "v1.2.3")
		for _, min := range []string{"v1.2.4", "v1.3.0", "v2.0.0", "v1.10.0"} {
			if err := sign(dir, "v1.2.3", min); err == nil || !strings.Contains(err.Error(), "lower") {
				t.Errorf("v1.2.3 signed although %s is published: %v", min, err)
			}
		}
		for _, min := range []string{"v1.2.3", "v1.2.2", "v0.9.9", "v1.1.99"} {
			if err := sign(dir, "v1.2.3", min); err != nil {
				t.Errorf("v1.2.3 refused with latest %s: %v", min, err)
			}
		}
	})
	t.Run("foreign url", func(t *testing.T) {
		dir := manifestFixture(t, "v1.2.3")
		mpath := filepath.Join(dir, "manifest.json")
		raw, _ := os.ReadFile(mpath)
		os.WriteFile(mpath, []byte(strings.Replace(string(raw), "/releases/download/v1.2.3/monoagentcli-linux-amd64", "/evil/monoagentcli-linux-amd64", 1)), 0o644)
		if err := sign(dir, "v1.2.3", ""); err == nil {
			t.Error("manifest with an off-release url signed")
		}
	})
}

// rewriteManifest edits manifest.json in the fixture dir and signs through the CI path.
func signRewritten(t *testing.T, edit func(m map[string]any)) error {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	newKeyEnv(t, priv)
	dir := manifestFixture(t, "v1.2.3")
	mpath := filepath.Join(dir, "manifest.json")
	raw, _ := os.ReadFile(mpath)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, _ := json.Marshal(m)
	os.WriteFile(mpath, out, 0o644)
	return run([]string{"sign", "-manifest", mpath, "-key-env", "TEST_RELEASE_KEY",
		"-assets-dir", dir, "-expect-version", "v1.2.3", "-url-prefix", prefixFor("v1.2.3")})
}

func TestSignManifestFieldValidation(t *testing.T) {
	soon := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	past := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	asset0 := func(m map[string]any) map[string]any { return m["assets"].([]any)[0].(map[string]any) }
	bad := map[string]func(m map[string]any){
		"asset url on another repo": func(m map[string]any) {
			asset0(m)["url"] = "https://github.com/evil/x/releases/download/v1.2.3/" + asset0(m)["name"].(string)
		},
		"asset url http": func(m map[string]any) {
			asset0(m)["url"] = "http://github.com/" + testRepo + "/releases/download/v1.2.3/" + asset0(m)["name"].(string)
		},
		"asset url other tag":  func(m map[string]any) { asset0(m)["url"] = prefixFor("v1.2.2") + asset0(m)["name"].(string) },
		"notes url other host": func(m map[string]any) { m["notes_url"] = "https://evil.example/notes" },
		"notes url http":       func(m map[string]any) { m["notes_url"] = "http://monoes.me/notes" },
		"notes url other repo": func(m map[string]any) { m["notes_url"] = "https://github.com/evil/x/releases/tag/v1.2.3" },
		"notes url dotdot":     func(m map[string]any) { m["notes_url"] = "https://github.com/" + testRepo + "/../evil/x/releases" },
		"notes url dotdot end": func(m map[string]any) { m["notes_url"] = "https://github.com/" + testRepo + "/releases/.." },
		"unknown field":        func(m map[string]any) { m["extra"] = true },
		"expired":              func(m map[string]any) { m["expires_at"] = past },
		"expires not RFC3339":  func(m map[string]any) { m["expires_at"] = "tomorrow" },
		"duplicate asset": func(m map[string]any) {
			a := m["assets"].([]any)
			m["assets"] = append(a, a[0])
		},
	}
	for name, edit := range bad {
		t.Run(name, func(t *testing.T) {
			if err := signRewritten(t, edit); err == nil {
				t.Error("signed")
			}
		})
	}
	good := map[string]func(m map[string]any){
		"unchanged":          func(m map[string]any) {},
		"future expiry":      func(m map[string]any) { m["expires_at"] = soon },
		"notes on monoes.me": func(m map[string]any) { m["notes_url"] = "https://monoes.me/releases/v1.2.3" },
	}
	for name, edit := range good {
		t.Run(name, func(t *testing.T) {
			if err := signRewritten(t, edit); err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}

func TestSignKeyFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX mode bits")
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := manifestFixture(t, "v1.2.3")
	mpath := filepath.Join(dir, "manifest.json")
	keyDir := t.TempDir()
	key := filepath.Join(keyDir, "key")
	os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600)
	link := filepath.Join(keyDir, "link")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	sign := func(path string) error { return run([]string{"sign", "-manifest", mpath, "-key-file", path}) }
	if err := sign(key); err != nil {
		t.Fatalf("0600 refused: %v", err)
	}
	if err := sign(link); err != nil {
		t.Fatalf("symlink to 0600 refused: %v", err)
	}
	for _, mode := range []os.FileMode{0o644, 0o640, 0o604} {
		os.Chmod(key, mode)
		if err := sign(key); err == nil || !strings.Contains(err.Error(), "0600") {
			t.Errorf("mode %04o accepted: %v", mode, err)
		}
		if err := sign(link); err == nil {
			t.Errorf("symlink to a %04o file accepted", mode)
		}
	}
}

func TestCheckStrict(t *testing.T) {
	dir := manifestFixture(t, "v1.2.3")
	mpath := filepath.Join(dir, "manifest.json")
	args := []string{"check", "-manifest", mpath, "-assets-dir", dir, "-expect-version", "v1.2.3", "-url-prefix", prefixFor("v1.2.3")}
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("not an asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(append(args, "-strict")); err == nil || !strings.Contains(err.Error(), "not listed") {
		t.Errorf("strict check passed with the unlisted fixture file(s): %v", err)
	}
	if err := run(args); err != nil {
		t.Errorf("non-strict check failed: %v", err)
	}
}

func TestDecodeManifestStrictRejectsJunk(t *testing.T) {
	dir := manifestFixture(t, "v1.2.3")
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeManifestStrict(raw); err != nil {
		t.Fatalf("clean manifest refused: %v", err)
	}
	trimmed := strings.TrimRight(string(raw), " \n")
	bad := map[string]string{
		"extra brace":       trimmed + "}",
		"extra bracket":     trimmed + "]",
		"second object":     trimmed + "\n" + trimmed,
		"garbage":           trimmed + " x",
		"case-variant dupe": strings.Replace(trimmed, `"version"`, `"Version": "v9.9.9", "version"`, 1),
		"upper-case dupe":   strings.Replace(trimmed, `"schema"`, `"SCHEMA": 2, "schema"`, 1),
	}
	for name, doc := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeManifestStrict([]byte(doc)); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestCheckStrictRejectsNonRegularEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := manifestFixture(t, "v1.2.3")
	mpath := filepath.Join(dir, "manifest.json")
	args := []string{"check", "-manifest", mpath, "-assets-dir", dir, "-expect-version", "v1.2.3", "-url-prefix", prefixFor("v1.2.3")}
	if err := run(append(args, "-strict")); err != nil {
		t.Fatalf("strict check of a clean upload dir (NOTICE is allowed; the extension zip is listed): %v", err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	err := run(append(args, "-strict"))
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("strict check with a symlink: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = run(append(args, "-strict"))
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("strict check with a directory: %v", err)
	}
}
