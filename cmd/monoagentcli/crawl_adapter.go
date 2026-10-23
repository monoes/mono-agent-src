package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/crawl"
)

// `crawl` and `crawl capture` live in internal/cli/crawl; this is their adapter.

func newCrawlCmd(cfg *globalConfig) *cobra.Command {
	return crawl.NewCommand(crawl.Deps{
		JSONOutput: func() bool { return cfg != nil && cfg.JSONOutput },
		RenderHTML: renderHTML,
	})
}
