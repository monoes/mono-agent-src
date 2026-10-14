package system

import (
	"context"
	"fmt"

	"github.com/mmcdole/gofeed"
	"github.com/monoes/mono-agent/internal/workflow"
)

// RSSReadNode fetches and parses an RSS/Atom feed.
// Type: "system.rss_read"
//
// With "since_last_run" set it emits only the entries newer than the ones an
// earlier run of the same node saw (rss_cursor.go); "baseline" makes the
// first such run emit nothing.
type RSSReadNode struct{}

func (n *RSSReadNode) Type() string { return "system.rss_read" }

func (n *RSSReadNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	feedURL, _ := config["url"].(string)
	if feedURL == "" {
		return nil, fmt.Errorf("system.rss_read: 'url' is required")
	}

	limit := 0
	if v, ok := config["limit"].(float64); ok {
		limit = int(v)
	}

	parser := gofeed.NewParser()
	feed, err := parser.ParseURLWithContext(feedURL, ctx)
	if err != nil {
		return nil, fmt.Errorf("system.rss_read: failed to fetch feed: %w", err)
	}

	items := feed.Items
	if sinceLastRun, _ := config["since_last_run"].(bool); sinceLastRun {
		items, err = itemsSinceLastRun(ctx, input, feedURL, items, workflow.PollBaseline(config))
		if err != nil {
			return nil, fmt.Errorf("system.rss_read: since_last_run: %w", err)
		}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	var resultItems []workflow.Item
	for _, fi := range items {
		data := map[string]interface{}{
			"title":       fi.Title,
			"link":        fi.Link,
			"description": fi.Description,
		}

		if fi.PublishedParsed != nil {
			data["published"] = fi.PublishedParsed.String()
		} else {
			data["published"] = fi.Published
		}

		if fi.Author != nil {
			data["author"] = fi.Author.Name
		} else {
			data["author"] = ""
		}

		cats := make([]interface{}, len(fi.Categories))
		for i, c := range fi.Categories {
			cats[i] = c
		}
		data["categories"] = cats

		resultItems = append(resultItems, workflow.NewItem(data))
	}

	return []workflow.NodeOutput{{Handle: "main", Items: resultItems}}, nil
}
