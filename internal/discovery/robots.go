package discovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the HTTP client discovery sources use for third-party responses: a request that
// stalls ends after 30 seconds instead of waiting on the context alone.
var Client = &http.Client{Timeout: 30 * time.Second}

// Largest third-party bodies a discovery run reads: a robots.txt, and a JSON API page.
const (
	MaxRobotsBytes = 1 << 20
	MaxAPIBytes    = 16 << 20
)

// LimitBody wraps a response body so reading past max bytes is an error, not an unbounded
// allocation.
func LimitBody(r io.Reader, max int64) io.Reader { return &limitedBody{r: r, left: max} }

type limitedBody struct {
	r    io.Reader
	left int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, fmt.Errorf("response is larger than the limit")
	}
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		return n, fmt.Errorf("response is larger than the limit")
	}
	return n, err
}

// CheckRobotsAllowed fetches robotsURL and returns an error if path is
// disallowed under a "User-agent: *" block. Shared by sources that hit a
// genuine JSON API (arbeitnow, jobicy) rather than scraping HTML pages —
// internal/discovery/sources/linkedin keeps its own private copy of this
// same check (written before this one existed) rather than being
// refactored to depend on it, to avoid any risk of regressing that
// package's already-tested scraping behavior for a stricter site.
func CheckRobotsAllowed(ctx context.Context, robotsURL, path string) error {
	robotsTxt, err := fetchRobotsTxt(ctx, robotsURL)
	if err != nil {
		return fmt.Errorf("checking robots.txt: %w", err)
	}
	if isDisallowedByRobots(robotsTxt, path) {
		return fmt.Errorf("robots.txt disallows %s — refusing to fetch", path)
	}
	return nil
}

// fetchRobotsTxt fetches robotsURL directly via net/http. A missing
// robots.txt (404) means nothing is disallowed.
func fetchRobotsTxt(ctx context.Context, robotsURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return "", fmt.Errorf("robots.txt request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MonoAgent/1.0)")
	resp, err := Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("robots.txt fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("robots.txt returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(LimitBody(resp.Body, MaxRobotsBytes))
	if err != nil {
		return "", fmt.Errorf("reading robots.txt: %w", err)
	}
	return string(body), nil
}

// isDisallowedByRobots is a minimal, prefix-match check of a robots.txt
// body for whether path is disallowed under a "User-agent: *" block — not
// a full RFC9309 implementation, sufficient for a hard stop/no-bypass gate.
func isDisallowedByRobots(robotsTxt, path string) bool {
	lines := strings.Split(robotsTxt, "\n")
	inWildcardBlock := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "user-agent":
			inWildcardBlock = val == "*"
		case "disallow":
			if inWildcardBlock && val != "" && strings.HasPrefix(path, val) {
				return true
			}
		}
	}
	return false
}
