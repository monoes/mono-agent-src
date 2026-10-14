package crawl

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/capture"
	capturecli "github.com/monoes/mono-agent/internal/cli/capture"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/crawlsite"
)

// newCrawlCaptureCmd walks a site and lands every page it finds in the
// capture inbox as an envelope (GLU-06).
//
// `crawl <url>` above renders one page in a real browser so Claude can
// write an action template from its DOM. This is the other job the word
// covers: reading a site into the document brain. It shares the inbox and
// the writer with `capture page`, so a crawled page and a clipped one are
// the same kind of document from here on — same directory layout, same
// meta.json, same dedupe key, differing only in meta.source.
func (d Deps) newCrawlCaptureCmd() *cobra.Command {
	var (
		depth             int
		maxPages          int
		delay             time.Duration
		timeout           time.Duration
		maxBytes          int64
		userAgent         string
		allowHosts        []string
		includeSubdomains bool
		robots            bool
		allowPrivateHosts bool
		collection        string
		tags              []string
		out               string
		quiet             bool
	)
	cmd := &cobra.Command{
		Use:   "capture <url>...",
		Short: "Crawl a site into the monomind inbox as capture envelopes",
		Long: "Walks a site over plain HTTP and writes one capture envelope per page into\n" +
			"~/.monomind/inbox/<timestamp>-<slug>/ — page.html, readable.md and meta.json,\n" +
			"the same contract the browser extension writes.\n" +
			"\n" +
			"The crawl stays on the seed URLs' hosts unless --allow-host widens it, follows\n" +
			"links --depth levels deep, stops after --max-pages, waits --delay between\n" +
			"requests to the same host, skips anything that is not HTML, and captures a\n" +
			"page that several URLs canonicalize to exactly once.\n" +
			"\n" +
			"robots.txt is honoured by default, including its Crawl-delay. --robots=false\n" +
			"turns that off for a site you own or have been asked to archive; you are then\n" +
			"the one answering for the traffic.\n" +
			"\n" +
			"Pages that only exist once JavaScript has run are not this command's job —\n" +
			"capture those from your browser with `capture page`.",
		Example: "  monoagentcli crawl capture https://docs.example.com/\n" +
			"  monoagentcli crawl capture https://example.com/blog --depth 2 --max-pages 200 --collection research\n" +
			"  monoagentcli crawl capture https://example.com --allow-host cdn.example.com --delay 2s --json",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if depth < 0 {
				return clicore.InvalidInput("--depth must not be negative, got %d", depth)
			}
			if maxPages <= 0 {
				return clicore.InvalidInput("--max-pages must be at least 1, got %d", maxPages)
			}
			if delay < 0 {
				return clicore.InvalidInput("--delay must not be negative, got %s", delay)
			}
			if maxBytes <= 0 {
				return clicore.InvalidInput("--max-bytes must be at least 1, got %d", maxBytes)
			}

			inbox := clicore.ExpandPath(out)
			if inbox == "" {
				inbox = capture.DefaultInbox()
			}
			jsonOut := d.JSONOutput()
			stderr := cmd.ErrOrStderr()

			opts := crawlsite.Options{
				Seeds:             args,
				MaxDepth:          depth,
				MaxPages:          maxPages,
				Delay:             delay,
				Timeout:           timeout,
				MaxBytes:          maxBytes,
				UserAgent:         userAgent,
				AllowHosts:        allowHosts,
				IncludeSubdomains: includeSubdomains,
				RespectRobots:     robots,
				AllowPrivateHosts: allowPrivateHosts,
				Collection:        collection,
				Tags:              tags,
				Sink:              &capture.Writer{Inbox: inbox},
			}
			if !quiet && !jsonOut {
				opts.OnEvent = func(e crawlsite.Event) {
					switch e.Kind {
					case crawlsite.EventSaved:
						fmt.Fprintf(stderr, "saved   %s\n", e.URL)
					case crawlsite.EventSkipped:
						fmt.Fprintf(stderr, "skipped %s — %s\n", e.URL, e.Reason)
					case crawlsite.EventFailed:
						fmt.Fprintf(stderr, "failed  %s — %s\n", e.URL, e.Reason)
					}
				}
			}

			crawler, err := crawlsite.New(opts)
			if err != nil {
				return clicore.InvalidInput("%s", err.Error())
			}
			sum, err := crawler.Run(cmd.Context())
			if err != nil {
				// A cancelled crawl still wrote what it wrote; report it.
				if sum == nil || len(sum.Pages) == 0 {
					return fmt.Errorf("crawling: %w", err)
				}
				fmt.Fprintf(stderr, "crawl stopped early: %v\n", err)
			}

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(sum)
			}
			return printCrawlSummary(cmd, sum, inbox)
		},
	}
	cmd.Flags().IntVar(&depth, "depth", crawlsite.DefaultMaxDepth, "How many links from each seed to follow (0 captures only the seeds)")
	cmd.Flags().IntVar(&maxPages, "max-pages", crawlsite.DefaultMaxPages, "Stop after this many captured pages")
	cmd.Flags().DurationVar(&delay, "delay", crawlsite.DefaultDelay, "Minimum wait between requests to the same host")
	cmd.Flags().DurationVar(&timeout, "timeout", crawlsite.DefaultTimeout, "Timeout for a single request")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", crawlsite.DefaultMaxBytes, "Maximum HTML bytes to read from one page")
	cmd.Flags().StringVar(&userAgent, "user-agent", crawlsite.DefaultUserAgent, "User-Agent to send, and the robots.txt group that applies")
	cmd.Flags().StringSliceVar(&allowHosts, "allow-host", nil, "Extra host the crawl may follow links into (repeatable)")
	cmd.Flags().BoolVar(&includeSubdomains, "include-subdomains", false, "Also follow links into subdomains of allowed hosts")
	cmd.Flags().BoolVar(&robots, "robots", true, "Honour robots.txt")
	// Default false: a crawl reaches loopback, link-local and private-network
	// addresses only on purpose. Without this an in-scope hostname that
	// resolves to a private address — an intranet name, or DNS rebinding —
	// turns a crawl of a hostile site into a request against this machine's
	// own network.
	cmd.Flags().BoolVar(&allowPrivateHosts, "allow-private-hosts", false, "Allow crawling loopback, link-local and private-network addresses (an intranet, or a local test server)")
	cmd.Flags().StringVar(&collection, "collection", "", "Collection to file every capture under")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "Tag to store on every capture (repeatable)")
	cmd.Flags().StringVar(&out, "out", "", "Inbox directory to write into (default: ~/.monomind/inbox)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Do not print per-page progress")
	return cmd
}

// printCrawlSummary renders the end of a crawl for a person.
func printCrawlSummary(cmd *cobra.Command, sum *crawlsite.Summary, inbox string) error {
	stdout := cmd.OutOrStdout()
	if sum == nil || len(sum.Pages) == 0 {
		fmt.Fprintf(stdout, "No pages captured into %s\n", inbox)
		return nil
	}
	table := clicore.NewPlainTable(stdout, []string{"TITLE", "URL", "DEPTH", "SIZE", "PATH"}, nil)
	var total int64
	for _, p := range sum.Pages {
		total += p.Bytes
		_ = table.Append([]string{
			capturecli.TruncateCell(p.Title, 40),
			capturecli.TruncateCell(p.URL, 60),
			fmt.Sprintf("%d", p.Depth),
			capturecli.HumanBytes(p.Bytes),
			p.Path,
		})
	}
	if err := table.Render(); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "\n%d page(s), %s into %s — %d fetched, %d duplicate(s), %d skipped, %d failed\n",
		len(sum.Pages), capturecli.HumanBytes(total), inbox, sum.Fetched, sum.Duplicates, len(sum.Skipped), len(sum.Failed))
	if sum.Truncated {
		fmt.Fprintf(cmd.ErrOrStderr(), "stopped at --max-pages; raise it to go further\n")
	}
	return nil
}
