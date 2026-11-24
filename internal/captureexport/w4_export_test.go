package captureexport

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/capture"
)

// R11-10: no absolute path and no OS host name unless the caller gave one.
func TestManifestCarriesNoAbsolutePathOrHostByDefault(t *testing.T) {
	inbox := seed(t, capture.Meta{URL: "https://example.com/a", Title: "A", CapturedAt: "2026-09-20T10:00:00Z"})
	_, man := exportToBytes(t, ExportOptions{Inbox: inbox})
	if man.Source.Inbox != filepath.Base(inbox) || strings.Contains(man.Source.Inbox, string(filepath.Separator)) {
		t.Errorf("inbox recorded as %q", man.Source.Inbox)
	}
	if man.Source.Host != "" {
		t.Errorf("host recorded as %q", man.Source.Host)
	}
	got := scrubInboxPath([]Note{{Name: "x", Reason: "not taken: open " + inbox + "/x/readable.md: denied"}}, inbox)
	if strings.Contains(got[0].Reason, inbox) {
		t.Errorf("reason keeps the path: %s", got[0].Reason)
	}
}
