package update

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/monoes/mono-agent/internal/release"
)

// Transport bounds for the legacy (unsigned-manifest) download path. They
// only limit how long and how much is read; what is verified and installed
// is decided elsewhere (verifyReleaseDigest). Variables so tests can shrink
// them.
var (
	// stallTimeout aborts a transfer that makes no progress for this long.
	stallTimeout = 60 * time.Second
	// totalTimeout bounds one whole transfer (same as internal/release).
	totalTimeout = 15 * time.Minute
	// apiTimeout bounds the latest-release lookup.
	apiTimeout = 30 * time.Second
	// maxBinaryBytes caps a release binary; maxSmallBytes caps checksum
	// lists and API payloads.
	maxBinaryBytes int64 = 512 << 20
	maxSmallBytes  int64 = 4 << 20
)

// apiClient replaces http.DefaultClient for the latest-release lookup.
func apiClient() *http.Client { return &http.Client{Timeout: apiTimeout} }

// stallReader cancels the request when no byte arrives for the stall window.
type stallReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.timer.Reset(s.d)
	}
	return n, err
}

// getCapped fetches url and returns at most max body bytes; a longer body,
// a stall, or exceeding totalTimeout is an error. Non-200 is an error.
func getCapped(url string, max int64) ([]byte, error) {
	if err := release.CheckLegacyURL(url); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), totalTimeout)
	defer cancel()
	stall := time.AfterFunc(stallTimeout, cancel)
	defer stall.Stop()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download failed: GitHub returned %d for %s", resp.StatusCode, url)
	}
	stall.Reset(stallTimeout)
	data, err := io.ReadAll(io.LimitReader(&stallReader{r: resp.Body, timer: stall, d: stallTimeout}, max+1))
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("download refused: %s is larger than %d bytes", url, max)
	}
	return data, nil
}
