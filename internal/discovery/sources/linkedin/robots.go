package linkedin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/monoes/mono-agent/internal/discovery"
)

// fetchRobotsTxt fetches robotsURL directly via net/http — not via
// crawl.FetchPage, which parses/re-serializes as HTML and would corrupt
// robots.txt's line-based plain-text structure. A missing robots.txt
// (404) means nothing is disallowed.
func fetchRobotsTxt(ctx context.Context, robotsURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return "", fmt.Errorf("linkedin: robots.txt request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MonoAgent/1.0)")
	resp, err := discovery.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("linkedin: robots.txt fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("linkedin: robots.txt returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(discovery.LimitBody(resp.Body, discovery.MaxRobotsBytes))
	if err != nil {
		return "", fmt.Errorf("linkedin: reading robots.txt: %w", err)
	}
	return string(body), nil
}

// isDisallowedByRobots is a minimal, prefix-match check of a robots.txt
// body for whether path is disallowed — not a full RFC9309 implementation,
// sufficient for a hard stop/no-bypass gate.
//
// Unlike the generic discovery.CheckRobotsAllowed (which only honors a
// "User-agent: *" block, deliberately ignoring named-bot blocks — see
// TestCheckRobotsAllowedIgnoresNamedBotBlocks), this LinkedIn-specific copy
// honors Disallow rules under EVERY User-agent block, named or wildcard.
// LinkedIn's real robots.txt has zero "User-agent: *" blocks — only ~36
// named-bot sections (LinkedInBot, Googlebot, Applebot, Bingbot, etc.),
// each disallowing /jobs-guest/ — and mono-agent's own User-Agent will
// never match one of those names. Restricting to the wildcard block would
// make this gate a permanent no-op for LinkedIn, its one real use case.
// Since this is explicitly a hard stop/no-bypass gate (not a scoping
// mechanism for "who may crawl what"), treating LinkedIn's robots.txt
// conservatively — disallowed under ANY block disallows the fetch — is the
// correct, safe behavior here.
func isDisallowedByRobots(robotsTxt, path string) bool {
	lines := strings.Split(robotsTxt, "\n")
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
		if key == "disallow" && val != "" && strings.HasPrefix(path, val) {
			return true
		}
	}
	return false
}
