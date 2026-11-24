// internal/discovery/sources/linkedin/robots_test.go
package linkedin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/monoes/mono-agent/internal/discovery"
)

// linkedInShapedRobotsTxt mirrors the real shape of
// https://www.linkedin.com/robots.txt: zero "User-agent: *" blocks, only
// named-bot sections (a small sample of the ~36 real ones), each
// disallowing /jobs-guest/.
const linkedInShapedRobotsTxt = `User-agent: LinkedInBot
Disallow: /jobs-guest/

User-agent: Googlebot
Disallow: /jobs-guest/

User-agent: Applebot
Disallow: /jobs-guest/

User-agent: Bingbot
Disallow: /jobs-guest/
`

func TestIsDisallowedByRobotsHonorsNamedBlocksForLinkedIn(t *testing.T) {
	// Regression test for Finding 1: mono-agent's own User-Agent
	// ("Mozilla/5.0 (compatible; MonoAgent/1.0)") never matches any of
	// these named blocks, and LinkedIn's real robots.txt has no wildcard
	// block at all. If isDisallowedByRobots only honored a "User-agent: *"
	// block (as the generic discovery.CheckRobotsAllowed deliberately
	// does), this hard-stop gate would be a permanent no-op for LinkedIn.
	got := isDisallowedByRobots(linkedInShapedRobotsTxt, "/jobs-guest/jobs/api/seeMoreJobPostings/search")
	if !got {
		t.Fatal("expected /jobs-guest/... to be disallowed under LinkedIn's real (named-block-only) robots.txt shape, got false")
	}
}

func TestIsDisallowedByRobotsAllowsUnrelatedPath(t *testing.T) {
	got := isDisallowedByRobots(linkedInShapedRobotsTxt, "/company/acme")
	if got {
		t.Fatal("expected an unrelated path not covered by any Disallow rule to be allowed, got disallowed")
	}
}

func TestIsDisallowedByRobotsStillHonorsWildcardBlock(t *testing.T) {
	// Sanity check: the ordinary wildcard-block case (as used by other
	// sources) must still work for LinkedIn's copy of the function too.
	robotsTxt := "User-agent: *\nDisallow: /jobs-guest/\n"
	if !isDisallowedByRobots(robotsTxt, "/jobs-guest/jobs/api/seeMoreJobPostings/search") {
		t.Fatal("expected wildcard-block Disallow to still be honored")
	}
}

func TestIsDisallowedByRobotsNoDisallowMeansAllowed(t *testing.T) {
	robotsTxt := "User-agent: LinkedInBot\nAllow: /\n"
	if isDisallowedByRobots(robotsTxt, "/jobs-guest/jobs/api/seeMoreJobPostings/search") {
		t.Fatal("expected no Disallow rules to mean allowed")
	}
}

func TestFetchRobotsTxtRefusesAnOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("User-agent: *\nAllow: /\n"))
		w.Write(make([]byte, discovery.MaxRobotsBytes+1))
	}))
	defer srv.Close()
	if _, err := fetchRobotsTxt(context.Background(), srv.URL+"/robots.txt"); err == nil {
		t.Fatal("an oversized robots.txt was accepted")
	}
}

func TestFetchRobotsTxtReadsAnOrdinaryBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(linkedInShapedRobotsTxt))
	}))
	defer srv.Close()
	body, err := fetchRobotsTxt(context.Background(), srv.URL+"/robots.txt")
	if err != nil || body != linkedInShapedRobotsTxt {
		t.Fatalf("body %q err %v", body, err)
	}
}
