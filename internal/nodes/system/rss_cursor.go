package system

import (
	"context"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/monoes/mono-agent/internal/workflow"
)

// rssItemID identifies an entry: guid, else link, else title plus date.
func rssItemID(fi *gofeed.Item) string {
	switch {
	case fi.GUID != "":
		return fi.GUID
	case fi.Link != "":
		return fi.Link
	default:
		return fi.Title + "\x00" + fi.Published
	}
}

func rssItemDate(fi *gofeed.Item) *time.Time {
	if fi.PublishedParsed != nil {
		return fi.PublishedParsed
	}
	return fi.UpdatedParsed
}

// itemsSinceLastRun returns the entries of items that the node's previous run
// of feedURL did not see, then stores the cursor for the next run (see
// workflow.PollCursor: a new id not dated before the newest entry seen). The
// first run returns every entry, or none with baseline. The cursor is per
// node and per feed URL, so changing the URL starts over.
func itemsSinceLastRun(ctx context.Context, input workflow.NodeInput, feedURL string, items []*gofeed.Item, baseline bool) ([]*gofeed.Item, error) {
	cur, err := workflow.OpenPollCursor(ctx, input, workflow.StateScopeNode, "rss_cursor:"+feedURL,
		workflow.PollCursorOptions{Baseline: baseline})
	if err != nil {
		return nil, err
	}
	fresh := make([]*gofeed.Item, 0, len(items))
	for _, fi := range items {
		if cur.IsNew(rssItemID(fi), rssItemDate(fi)) {
			fresh = append(fresh, fi)
		}
	}
	if err := cur.Save(ctx); err != nil {
		return nil, err
	}
	return fresh, nil
}
