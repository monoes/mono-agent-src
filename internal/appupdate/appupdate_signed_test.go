package appupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/release"
)

// signedRig serves a signed manifest for the linux app tarball over TLS.
type signedRig struct {
	srv        *httptest.Server
	rc         *release.Client
	tarball    []byte
	servedBody []byte // what /app answers
	manifest   []byte
	sig        []byte
}

func newSignedRig(t *testing.T, version string) *signedRig {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	r := &signedRig{tarball: linuxTarball(t, map[string]string{"MonoAgent-linux-amd64": "new app", "monoagentcli": "new cli"})}
	r.servedBody = r.tarball
	mux := http.NewServeMux()
	r.srv = httptest.NewTLSServer(mux)
	t.Cleanup(r.srv.Close)
	m := release.Manifest{Schema: 1, Version: version, ReleasedAt: "2026-10-08T10:00:00Z", Assets: []release.Asset{{
		OS: "linux", Arch: "amd64", Name: "MonoAgent-linux-amd64.tar.gz", URL: r.srv.URL + "/app",
		SHA256: sha256hex(r.tarball), Size: int64(len(r.tarball)), Kind: "app"}}}
	r.manifest, _ = json.Marshal(m)
	r.sig = []byte(release.KeyID(pub) + " " + base64.StdEncoding.EncodeToString(ed25519.Sign(priv, r.manifest)))
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, _ *http.Request) { w.Write(r.manifest) })
	mux.HandleFunc("/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) { w.Write(r.sig) })
	mux.HandleFunc("/app", func(w http.ResponseWriter, _ *http.Request) { w.Write(r.servedBody) })
	r.rc = &release.Client{HTTP: r.srv.Client(), Keys: []release.Key{{ID: release.KeyID(pub), Public: pub}},
		ManifestURLs: []string{r.srv.URL + "/manifest.json"}, Hosts: []string{"127.0.0.1"}, RedirectHosts: []string{"127.0.0.1"}}
	return r
}

func (r *signedRig) updater(exe string) Updater {
	return Updater{Release: r.rc, GOOS: "linux", GOARCH: "amd64", Exe: exe,
		// A legacy endpoint that must never be reached when the signed path answers.
		APIURL: "https://127.0.0.1:1/never", StartDetached: func(string, ...string) error { return nil }}
}

func TestSignedAppUpdateInstalls(t *testing.T) {
	r := newSignedRig(t, "v2.0.0")
	exe := linuxInstall(t, "monoagentcli")
	res, err := r.updater(exe).Run()
	if err != nil || res.NewVersion != "v2.0.0" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if readFile(t, exe) != "new app" || readFile(t, filepath.Join(filepath.Dir(exe), "monoagentcli")) != "new cli" {
		t.Fatal("not installed")
	}
}

func TestSignedAppUpdateFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		desc, want string
		mutate     func(*signedRig)
		current    string
	}{
		{"tampered asset", "sha256 mismatch", func(r *signedRig) {
			b := append([]byte(nil), r.tarball...)
			b[len(b)-1] ^= 0xff
			r.servedBody = b
		}, ""},
		{"bad signature", "signature does not match", func(r *signedRig) { r.manifest = []byte(strings.Replace(string(r.manifest), "v2.0.0", "v3.0.0", 1)) }, ""},
		{"downgrade", "refusing to downgrade", func(*signedRig) {}, "v3.0.0"},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			r := newSignedRig(t, "v2.0.0")
			tc.mutate(r)
			exe := linuxInstall(t, "monoagentcli")
			u := r.updater(exe)
			u.Current = tc.current
			_, err := u.Run()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if readFile(t, exe) != "old app" || readFile(t, filepath.Join(filepath.Dir(exe), "monoagentcli")) != "old cli" {
				t.Fatal("installation changed")
			}
		})
	}
}

func TestSignedAppUpdateForceAllowsDowngrade(t *testing.T) {
	r := newSignedRig(t, "v2.0.0")
	u := r.updater(linuxInstall(t, "monoagentcli"))
	u.Current, u.Force = "v3.0.0", true
	if _, err := u.Run(); err != nil {
		t.Fatal(err)
	}
}

func TestNoPinnedKeyFallsBackOnlyWhileLegacyAllowed(t *testing.T) {
	tgz := linuxTarball(t, map[string]string{"MonoAgent-linux-amd64": "new app", "monoagentcli": "new cli"})
	srv := fakeAppRelease{assets: map[string][]byte{"MonoAgent-linux-amd64.tar.gz": tgz}}.server(t)
	var started []string
	u := testUpdater(srv, "linux", "amd64", linuxInstall(t, "monoagentcli"), &started)
	u.Release = &release.Client{} // no pinned key
	if _, err := u.Run(); err != nil {
		t.Fatalf("legacy fallback: %v", err)
	}

	release.AllowLegacyGitHubUpdates = false
	t.Cleanup(func() { release.AllowLegacyGitHubUpdates = true })
	exe := linuxInstall(t, "monoagentcli")
	u = testUpdater(srv, "linux", "amd64", exe, &started)
	u.Release = &release.Client{}
	if _, err := u.Run(); err == nil || !strings.Contains(err.Error(), "cannot verify") {
		t.Fatalf("legacy off: %v", err)
	}
	if readFile(t, exe) != "old app" {
		t.Fatal("installed without verification")
	}
}

// F4: the legacy path takes only https on GitHub's hosts (loopback http for tests).
func TestLegacyPathRefusesForeignHosts(t *testing.T) {
	u := Updater{APIURL: "https://evil.example/latest"}
	if _, err := u.fetchRelease(); err == nil || !strings.Contains(err.Error(), "allowed host") {
		t.Fatalf("fetchRelease err = %v", err)
	}
	if _, err := u.getAll("http://example.com/x", 10); err == nil {
		t.Fatal("plain http to a foreign host must be refused")
	}
}
