package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/release"
)

// signedExtensionRelease is a fake release served over TLS: manifest.json
// signed with a test key, its .sig, the extension zip and SHA256SUMS.txt.
type signedExtensionRelease struct {
	srv      *httptest.Server
	priv     ed25519.PrivateKey
	manifest []byte
	sig      []byte
	zip      []byte // what the server hands out
	sums     string // SHA256SUMS.txt digest for the zip
}

func (r *signedExtensionRelease) setManifest(t *testing.T, m release.Manifest) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	r.manifest = b
	pub := r.priv.Public().(ed25519.PublicKey)
	r.sig = []byte(release.KeyID(pub) + " " + base64.StdEncoding.EncodeToString(ed25519.Sign(r.priv, b)) + "\n")
}

// extensionManifest lists the zip as the "extension" asset of version, with
// the digest and size of signedZip.
func (r *signedExtensionRelease) extensionManifest(version string, signedZip []byte) release.Manifest {
	sum := sha256.Sum256(signedZip)
	return release.Manifest{Schema: 1, Version: version, ReleasedAt: "2026-10-13T00:00:00Z", NotesURL: "https://monoes.me/n",
		Assets: []release.Asset{{Name: extensionZipAsset, Kind: "extension",
			URL:    r.srv.URL + "/releases/download/" + version + "/" + extensionZipAsset,
			SHA256: hex.EncodeToString(sum[:]), Size: int64(len(signedZip))}}}
}

func newSignedExtensionRelease(t *testing.T, zipData []byte) *signedExtensionRelease {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := &signedExtensionRelease{priv: priv, zip: zipData, sums: sha256Hex(zipData)}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/manifest.json"):
			_, _ = w.Write(r.manifest)
		case strings.HasSuffix(req.URL.Path, "/manifest.json.sig"):
			_, _ = w.Write(r.sig)
		case strings.HasSuffix(req.URL.Path, "/"+extensionZipAsset):
			_, _ = w.Write(r.zip)
		case strings.HasSuffix(req.URL.Path, "/"+sha256SumsAssetName):
			_, _ = w.Write([]byte(r.sums + "  " + extensionZipAsset + "\n"))
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.srv.Close)

	oldURL, oldClient, oldGet := extensionReleasesURL, extensionManifestClient, httpGetAll
	extensionReleasesURL = r.srv.URL + "/releases"
	// The SHA256SUMS.txt fallback downloads over plain httpGetAll; give it the
	// test server's TLS client.
	httpGetAll = func(u string) ([]byte, error) {
		resp, err := r.srv.Client().Get(u)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, u)
		}
		return io.ReadAll(resp.Body)
	}
	extensionManifestClient = func(manifestURL string) *release.Client {
		return &release.Client{
			HTTP: r.srv.Client(), Keys: []release.Key{{ID: release.KeyID(pub), Public: pub}},
			ManifestURLs: []string{manifestURL},
			Hosts:        []string{"127.0.0.1"}, RedirectHosts: []string{"127.0.0.1"},
			MaxAssetBytes: maxExtensionZip,
		}
	}
	t.Cleanup(func() { extensionReleasesURL, extensionManifestClient, httpGetAll = oldURL, oldClient, oldGet })
	r.setManifest(t, r.extensionManifest("v1.2.3", zipData))
	return r
}

func extensionFixture(t *testing.T) []byte {
	return fixtureExtensionZip(t, map[string]string{"chrome-extension/manifest.json": `{"manifest_version":3}`})
}

func TestExtensionDownloadVerifiesTheSignedManifest(t *testing.T) {
	data := extensionFixture(t)
	newSignedExtensionRelease(t, data)
	var out bytes.Buffer
	got, err := downloadExtensionZip(&out, "v1.2.3")
	if err != nil {
		t.Fatalf("download: %v\n%s", err, out.String())
	}
	if !bytes.Equal(got, data) {
		t.Fatal("returned bytes differ from the release zip")
	}
	for _, want := range []string{"Manifest signature verified (release v1.2.3)", "Checksum verified against the signed manifest"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), sha256SumsAssetName) {
		t.Errorf("the signed path must not use %s:\n%s", sha256SumsAssetName, out.String())
	}
}

func TestExtensionInstallFromTheSignedManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	newSignedExtensionRelease(t, extensionFixture(t))
	// A development build reads the latest release's manifest.
	out, err := runExtensionInstall(t)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Manifest signature verified") {
		t.Errorf("output lacks the signature line:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "extension", "manifest.json")); err != nil {
		t.Errorf("extension not unpacked: %v", err)
	}
}

func TestExtensionDownloadRejectsATamperedZip(t *testing.T) {
	signed := extensionFixture(t)
	r := newSignedExtensionRelease(t, signed)
	// The server hands out other bytes of the same size (so the sha256 check,
	// not the size check, refuses them); SHA256SUMS.txt is made to match
	// them, so only the signed manifest can catch it.
	r.zip = append([]byte{}, signed...)
	r.zip[len(r.zip)/2] ^= 0xff
	r.sums = sha256Hex(r.zip)

	home := t.TempDir()
	t.Setenv("HOME", home)
	var out bytes.Buffer
	if _, err := downloadExtensionZip(&out, "v1.2.3"); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("want a sha256 mismatch, got %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "extension")); !os.IsNotExist(err) {
		t.Errorf("something was installed: %v", err)
	}
}

func TestExtensionDownloadRejectsABadSignature(t *testing.T) {
	data := extensionFixture(t)
	cases := map[string]func(r *signedExtensionRelease){
		"manifest altered after signing": func(r *signedExtensionRelease) {
			r.manifest = bytes.Replace(r.manifest, []byte("2026-10-13"), []byte("2026-10-14"), 1)
		},
		"signed by another key": func(r *signedExtensionRelease) {
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			keyID := strings.Fields(string(r.sig))[0]
			r.sig = []byte(keyID + " " + base64.StdEncoding.EncodeToString(ed25519.Sign(other, r.manifest)) + "\n")
		},
		"malformed signature file": func(r *signedExtensionRelease) { r.sig = []byte("garbage") },
	}
	for name, spoil := range cases {
		t.Run(name, func(t *testing.T) {
			r := newSignedExtensionRelease(t, data)
			spoil(r)
			var out bytes.Buffer
			_, err := downloadExtensionZip(&out, "v1.2.3")
			if err == nil || !strings.Contains(err.Error(), "signed release manifest was rejected") {
				t.Fatalf("want a rejected manifest, got %v\n%s", err, out.String())
			}
			// SHA256SUMS.txt matches the zip here: a failed signature must not
			// fall back to it.
			if strings.Contains(out.String(), "Checksum verified") {
				t.Errorf("fell back after a failed signature:\n%s", out.String())
			}
		})
	}
}

func TestExtensionDownloadRejectsAnotherReleasesManifest(t *testing.T) {
	data := extensionFixture(t)
	r := newSignedExtensionRelease(t, data)
	r.setManifest(t, r.extensionManifest("v1.0.0", data))
	var out bytes.Buffer
	if _, err := downloadExtensionZip(&out, "v1.2.3"); err == nil || !strings.Contains(err.Error(), "is for v1.0.0, not v1.2.3") {
		t.Fatalf("want a version mismatch, got %v", err)
	}
}

func TestExtensionDownloadFallsBackForAnOlderManifest(t *testing.T) {
	data := extensionFixture(t)
	r := newSignedExtensionRelease(t, data)
	m := r.extensionManifest("v1.2.3", data)
	m.Assets = []release.Asset{{OS: "linux", Arch: "amd64", Name: "monoagentcli-linux-amd64", Kind: "cli",
		URL: r.srv.URL + "/x", SHA256: strings.Repeat("0", 64), Size: 1}}
	r.setManifest(t, m)

	var out bytes.Buffer
	got, err := downloadExtensionZip(&out, "v1.2.3")
	if err != nil {
		t.Fatalf("download: %v\n%s", err, out.String())
	}
	if !bytes.Equal(got, data) {
		t.Fatal("returned bytes differ from the release zip")
	}
	for _, want := range []string{"lists no extension in it (an older release); checking against " + sha256SumsAssetName, "Checksum verified: "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	// The fallback still refuses a zip that does not match SHA256SUMS.txt.
	r.sums = strings.Repeat("0", 64)
	if _, err := downloadExtensionZip(&bytes.Buffer{}, "v1.2.3"); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("want a checksum mismatch on the fallback, got %v", err)
	}
}
