// Package crawl is `monoagentcli crawl`: `crawl <url>` opens one page in your browser through the extension and
// saves its HTML for writing an automation package, and `crawl capture <url>...` walks a site
// over plain HTTP into the capture inbox. Everything the commands need from the CLI around them
// comes in through Deps, so they can be tested in isolation.
package crawl

import (
	"context"
	"time"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// RenderHTML opens rawURL in the user's browser through the extension, waits for
	// the page to render and returns its HTML.
	RenderHTML func(ctx context.Context, rawURL string, wait time.Duration) (string, error)
}
