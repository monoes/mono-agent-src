// Package release verifies and fetches signed release manifests.
//
// A release publishes manifest.json and manifest.json.sig (one line,
// "<key-id> <base64 Ed25519 signature over the exact bytes of manifest.json>").
// The client pins only public keys (keys.go). Update integrity is: signature
// over the manifest, then the manifest's sha256 and size for each asset.
package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ReleasesRepo is the public releases-only GitHub repository (the source
// repository is private and never named in a URL a client uses).
const ReleasesRepo = "monoes/mono-agent"

// ManifestURL is where monoes.me serves the latest signed manifest.
const ManifestURL = "https://monoes.me/mono-agent/releases/latest/manifest.json"

// RepoManifestURL is the same manifest as a releases-repo asset.
const RepoManifestURL = "https://github.com/" + ReleasesRepo + "/releases/latest/download/manifest.json"

// AllowLegacyGitHubUpdates lets the updaters fall back to the GitHub API
// (the releases' SHA256SUMS.txt over TLS) when the signed manifest is
// unavailable. It is false in every shipped build: the fallback is unsigned
// (TLS plus SHA256SUMS.txt only) with no expiry or downgrade protection, so
// anyone who could block the signed manifest could force the weaker path.
// (A var only so tests of the remaining legacy code can turn it on.)
var AllowLegacyGitHubUpdates = false

// SchemaVersion is the only manifest schema this client understands.
const SchemaVersion = 1

var (
	// ErrUnavailable means the signed path cannot be used (no pinned key, or
	// the manifest could not be fetched): the caller may fall back to the
	// legacy path while AllowLegacyGitHubUpdates is true.
	ErrUnavailable = errors.New("signed release manifest unavailable")
	// ErrUntrusted means a manifest or download was fetched but failed a
	// check. It is never a reason to fall back.
	ErrUntrusted = errors.New("release failed verification")
)

func untrusted(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUntrusted, fmt.Sprintf(format, a...))
}

// Asset is one downloadable file of a release.
type Asset struct {
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Kind   string `json:"kind"` // "cli" or "app"
}

// Manifest is manifest.json.
type Manifest struct {
	Schema     int    `json:"schema"`
	Version    string `json:"version"`
	ReleasedAt string `json:"released_at"`
	NotesURL   string `json:"notes_url"`
	// ExpiresAt (RFC 3339, optional, signed with the rest) is the time after
	// which clients reject this manifest.
	ExpiresAt string  `json:"expires_at,omitempty"`
	Assets    []Asset `json:"assets"`
}

// VerifyWith checks sig (a "<key-id> <base64 signature>" line) over the
// exact manifest bytes against keys (normally PinnedKeys()) and parses the
// manifest.
func VerifyWith(keys []Key, manifest, sig []byte) (*Manifest, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: this build pins no release key", ErrUnavailable)
	}
	f := strings.Fields(string(sig))
	if len(f) != 2 || len(f[0]) != 16 {
		return nil, untrusted("signature file must be \"<key-id> <base64 signature>\"")
	}
	raw, err := base64.StdEncoding.DecodeString(f[1])
	if err != nil || len(raw) != ed25519.SignatureSize {
		return nil, untrusted("signature is not a base64 ed25519 signature")
	}
	if revokedReleaseKeyIDs[f[0]] {
		return nil, untrusted("signed by key %s, which this build has revoked", f[0])
	}
	var key *Key
	for i := range keys {
		if keys[i].ID == f[0] {
			key = &keys[i]
		}
	}
	if key == nil {
		return nil, untrusted("signed by key %s, which this build does not pin", f[0])
	}
	if !ed25519.Verify(key.Public, manifest, raw) {
		return nil, untrusted("signature does not match the manifest (key %s)", f[0])
	}
	return parseManifest(manifest)
}

func parseManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, untrusted("manifest is not valid JSON: %v", err)
	}
	if m.Schema != SchemaVersion {
		return nil, untrusted("manifest schema %d is not supported (want %d); update by hand", m.Schema, SchemaVersion)
	}
	if strings.TrimSpace(m.Version) == "" {
		return nil, untrusted("manifest has no version")
	}
	if _, err := time.Parse(time.RFC3339, m.ReleasedAt); err != nil {
		return nil, untrusted("manifest released_at is not RFC 3339")
	}
	if m.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, m.ExpiresAt); err != nil {
			return nil, untrusted("manifest expires_at is not RFC 3339")
		}
	}
	return &m, nil
}

// CheckExpiry rejects a manifest whose expires_at is in the past.
func (m *Manifest) CheckExpiry(now time.Time) error {
	if m.ExpiresAt == "" {
		return nil
	}
	exp, err := time.Parse(time.RFC3339, m.ExpiresAt)
	if err != nil {
		return untrusted("manifest expires_at is not RFC 3339")
	}
	if now.After(exp) {
		return untrusted("manifest expired at %s; ask the maintainers for a fresh release", m.ExpiresAt)
	}
	return nil
}

// Asset returns the asset with this name for os/arch/kind.
func (m *Manifest) Asset(name, goos, goarch, kind string) (Asset, error) {
	for _, a := range m.Assets {
		if a.Name == name && a.OS == goos && a.Arch == goarch && a.Kind == kind {
			return a, nil
		}
	}
	return Asset{}, fmt.Errorf("release %s has no %s asset %q for %s/%s", m.Version, kind, name, goos, goarch)
}

// Select returns the one asset of kind for os/arch; none or several is an error.
func (m *Manifest) Select(goos, goarch, kind string) (Asset, error) {
	var found []Asset
	for _, a := range m.Assets {
		if a.OS == goos && a.Arch == goarch && a.Kind == kind {
			found = append(found, a)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return Asset{}, fmt.Errorf("release %s has no %s asset for %s/%s", m.Version, kind, goos, goarch)
	}
	return Asset{}, fmt.Errorf("release %s has %d %s assets for %s/%s; ask by name", m.Version, len(found), kind, goos, goarch)
}

// CheckAsset verifies downloaded bytes against the asset's size and sha256.
func CheckAsset(a Asset, data []byte) error {
	if int64(len(data)) != a.Size {
		return untrusted("%s is %d bytes, the manifest says %d", a.Name, len(data), a.Size)
	}
	sum := sha256.Sum256(data)
	want, err := hex.DecodeString(a.SHA256)
	if err != nil || len(want) != sha256.Size {
		return untrusted("%s has a malformed sha256 in the manifest", a.Name)
	}
	if hex.EncodeToString(sum[:]) != hex.EncodeToString(want) {
		return untrusted("%s: sha256 mismatch (manifest %s, download %s)", a.Name, a.SHA256, hex.EncodeToString(sum[:]))
	}
	return nil
}

// CheckURL requires an https URL on one of hosts.
func CheckURL(raw string, hosts []string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return untrusted("%q is not an https URL", raw)
	}
	for _, h := range hosts {
		if strings.EqualFold(u.Hostname(), h) {
			return nil
		}
	}
	return untrusted("%s is not on an allowed host", u.Hostname())
}

// DownloadHostsLegacy are the hosts a release asset (and its redirects)
// is served from; the legacy path accepts nothing else.
var DownloadHostsLegacy = []string{"github.com", "api.github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"}

// CheckLegacyURL is CheckURL for the legacy path: https on one of
// DownloadHostsLegacy, or plain http to a loopback address (local test servers).
func CheckLegacyURL(raw string) error {
	if u, err := url.Parse(raw); err == nil && u.Scheme == "http" {
		if ip := net.ParseIP(u.Hostname()); ip != nil && ip.IsLoopback() {
			return nil
		}
	}
	return CheckURL(raw, DownloadHostsLegacy)
}

// Compare orders dotted release versions numerically ("v0.10.0" > "v0.9.9").
func Compare(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(strings.TrimSpace(a), "v"), ".")
	pb := strings.Split(strings.TrimPrefix(strings.TrimSpace(b), "v"), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := part(pa, i), part(pb, i)
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	return 0
}

func part(p []string, i int) int {
	if i >= len(p) {
		return 0
	}
	n := 0
	for _, r := range p[i] {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// CheckDowngrade refuses to install a version lower than current unless
// force is set. A dev build (no release version) has nothing to compare.
func CheckDowngrade(version, current string, force bool) error {
	c := strings.TrimSpace(current)
	if force || c == "" || c == "dev" || strings.Contains(c, "-g") {
		return nil
	}
	if Compare(version, c) < 0 {
		return untrusted("release %s is older than the installed %s; refusing to downgrade (--force overrides)", version, current)
	}
	return nil
}
