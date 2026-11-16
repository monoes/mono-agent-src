package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/statedir"
)

const (
	maxManifestBytes = 1 << 20
	maxSigBytes      = 4 << 10
	maxAssetBytes    = 512 << 20
)

// Client fetches and verifies the signed manifest and its assets.
type Client struct {
	HTTP         *http.Client // nil: a client with a 15 minute timeout
	Keys         []Key
	ManifestURLs []string // tried in order
	// Hosts may serve the manifest and assets; RedirectHosts (Hosts plus the
	// release CDN) may be redirected to. Both are hostnames.
	Hosts         []string
	RedirectHosts []string
	MaxAssetBytes int64 // 0: 512 MiB
	// StatePath is a JSON file holding the highest manifest version and
	// released_at ever accepted; a manifest below either is refused as a
	// rollback unless Force. "" disables the check (tests).
	StatePath string
	Force     bool             // allow a version/released_at below the recorded highest
	Now       func() time.Time // nil: time.Now
}

// LiveReleaseTestEnv opts a test binary into the live manifest: under go test
// DefaultClient is otherwise unkeyed, so no test reaches the real release
// server or writes release-state.json by accident.
const LiveReleaseTestEnv = "MONOAGENT_TEST_LIVE_RELEASE"

// DefaultClient is the production client: pinned keys, monoes.me first and
// the releases repo second. Under go test (unless LiveReleaseTestEnv=1) it is
// an unkeyed client: Available is false, so the signed path never runs.
func DefaultClient() *Client {
	if testing.Testing() && os.Getenv(LiveReleaseTestEnv) != "1" {
		return &Client{}
	}
	return &Client{
		Keys:          PinnedKeys(),
		StatePath:     DefaultStatePath(),
		ManifestURLs:  []string{ManifestURL, RepoManifestURL},
		Hosts:         []string{"monoes.me", "github.com"},
		RedirectHosts: []string{"monoes.me", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"},
	}
}

// Available reports whether any key is pinned; without one the signed path
// is not tried at all (no network).
func (c *Client) Available() bool { return len(c.Keys) > 0 }

func (c *Client) httpClient() *http.Client {
	base := c.HTTP
	if base == nil {
		base = &http.Client{Timeout: 15 * time.Minute}
	}
	cp := *base
	cp.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return untrusted("too many redirects")
		}
		if err := CheckURL(req.URL.String(), c.RedirectHosts); err != nil {
			return fmt.Errorf("redirect refused: %w", err)
		}
		return nil
	}
	return &cp
}

// get reads at most max bytes of a 200 response; a longer body is an error.
func (c *Client) get(ctx context.Context, raw string, max int64) ([]byte, error) {
	if err := CheckURL(raw, c.Hosts); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode, raw)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, untrusted("%s is larger than %d bytes", raw, max)
	}
	return b, nil
}

// Latest returns the verified latest manifest. ErrUnavailable means nothing
// signed could be used (no pinned key, or no manifest URL answered); any
// other error is a failed check and must not fall back.
func (c *Client) Latest(ctx context.Context) (*Manifest, error) {
	if !c.Available() {
		return nil, fmt.Errorf("%w: this build pins no release key", ErrUnavailable)
	}
	var last error
	for _, mu := range c.ManifestURLs {
		body, err := c.get(ctx, mu, maxManifestBytes)
		if err != nil {
			if errors.Is(err, ErrUntrusted) {
				return nil, err
			}
			last = err
			continue
		}
		su, err := sigURL(mu)
		if err != nil {
			return nil, err
		}
		sig, err := c.get(ctx, su, maxSigBytes)
		if err != nil {
			if errors.Is(err, ErrUntrusted) {
				return nil, err
			}
			last = err
			continue
		}
		m, err := VerifyWith(c.Keys, body, sig)
		if err != nil {
			return nil, err
		}
		if err := c.checkFresh(m); err != nil {
			return nil, err
		}
		return m, nil
	}
	return nil, fmt.Errorf("%w: %w", ErrUnavailable, last)
}

func sigURL(manifestURL string) (string, error) {
	u, err := url.Parse(manifestURL)
	if err != nil {
		return "", err
	}
	u.Path += ".sig"
	return u.String(), nil
}

// Download fetches a and returns its bytes only after size and sha256 pass.
func (c *Client) Download(ctx context.Context, a Asset) ([]byte, error) {
	limit := c.MaxAssetBytes
	if limit <= 0 {
		limit = maxAssetBytes
	}
	if a.Size <= 0 || a.Size > limit {
		return nil, untrusted("%s declares %d bytes (allowed 1..%d)", a.Name, a.Size, limit)
	}
	data, err := c.get(ctx, a.URL, a.Size)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", a.Name, err)
	}
	if err := CheckAsset(a, data); err != nil {
		return nil, err
	}
	return data, nil
}

// DefaultStatePath is ~/.monoagent/release-state.json, the CLI's global
// (profile-independent) state directory; "" when the home is unknown.
func DefaultStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".monoagent", "release-state.json")
}

type state struct {
	Version    string `json:"version"`
	ReleasedAt string `json:"released_at"`
}

// checkFresh enforces expires_at, refuses a rollback below the highest
// accepted version / released_at, and records the new highest values.
func (c *Client) checkFresh(m *Manifest) error {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	if err := m.CheckExpiry(now()); err != nil {
		return err
	}
	if c.StatePath == "" {
		return nil
	}
	var st state
	if b, err := os.ReadFile(c.StatePath); err == nil {
		if json.Unmarshal(b, &st) != nil {
			st = state{} // unreadable state must not brick updates
		}
	}
	rel, _ := time.Parse(time.RFC3339, m.ReleasedAt)
	seen, seenErr := time.Parse(time.RFC3339, st.ReleasedAt)
	if !c.Force {
		if st.Version != "" && Compare(m.Version, st.Version) < 0 {
			return untrusted("manifest version %s is older than %s, which was already accepted; refusing a rollback (--force overrides)", m.Version, st.Version)
		}
		if seenErr == nil && rel.Before(seen) {
			return untrusted("manifest released_at %s is older than %s, which was already accepted; refusing a rollback (--force overrides)", m.ReleasedAt, st.ReleasedAt)
		}
	}
	next := st
	if st.Version == "" || Compare(m.Version, st.Version) > 0 {
		next.Version = m.Version
	}
	if seenErr != nil || rel.After(seen) {
		next.ReleasedAt = m.ReleasedAt
	}
	if next != st {
		if b, err := json.Marshal(next); err == nil {
			_ = statedir.WriteFile(c.StatePath, b)
		}
	}
	return nil
}
