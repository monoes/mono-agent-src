package crawl

import (
	"strings"
	"testing"
)

// TestCrawlRejectsNonHTTPScheme guards the scheme allowlist: file://,
// javascript:, chrome://, and friends must be refused before any browser
// launch, not handed to Rod.
//
// The refusal is matched exactly, because a box with no Chrome fails for
// its own reasons and says so in a message that also contains "http" — the
// download URL. Matching that would pass on this machine with the scheme
// check deleted, and on a machine WITH Chrome the same test would quietly
// be opening file:///etc/passwd.
func TestCrawlRejectsNonHTTPScheme(t *testing.T) {
	t.Parallel()
	for _, rawURL := range []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"chrome://settings",
		"ftp://example.com/pub",
		"data:text/html,<h1>hi",
	} {
		cmd := NewCommand(Deps{JSONOutput: func() bool { return false }})
		cmd.SetArgs([]string{rawURL})
		err := cmd.Execute()
		if err == nil {
			t.Errorf("crawl %q: expected scheme rejection, got nil", rawURL)
			continue
		}
		if !strings.Contains(err.Error(), "unsupported URL scheme") {
			t.Errorf("crawl %q: error = %v, want the scheme refusal", rawURL, err)
		}
		if strings.Contains(err.Error(), "browser") || strings.Contains(err.Error(), "launch") {
			t.Errorf("crawl %q reached for a browser first: %v", rawURL, err)
		}
	}
}

// TestCrawlCapsWait guards the --wait bounds: negative values or anything
// over 120s must be refused up front (a typo like --wait 1200 would
// otherwise hang the command for 20 minutes).
func TestCrawlCapsWait(t *testing.T) {
	t.Parallel()
	for _, wait := range []string{"-1", "121", "9999"} {
		cmd := NewCommand(Deps{JSONOutput: func() bool { return false }})
		cmd.SetArgs([]string{"https://example.com", "--wait", wait})
		err := cmd.Execute()
		if err == nil {
			t.Errorf("--wait %s: expected rejection, got nil", wait)
			continue
		}
		if !strings.Contains(err.Error(), "--wait") {
			t.Errorf("--wait %s: error = %v, want it to name --wait", wait, err)
		}
	}
}
