package capturesummary

import (
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/capture"
)

// PrivateTag marks a capture whose text must stay on this machine.
const PrivateTag = "private"

// refusal says why a capture's text may not be sent to the summary runtime
// (possibly a remote model), or "" when it may. A recording holds typed
// values and page DOM from logged-in pages; a capture the user tagged
// private asked to stay local. Neither is summarized even when it carries a
// summarize request (R11-13).
func refusal(meta capture.Meta) string {
	if meta.Source == capture.SourceRecording {
		return "not summarized: a recording holds typed values and page DOM, and stays on this machine"
	}
	for _, tag := range meta.Tags {
		if strings.EqualFold(strings.TrimSpace(tag), PrivateTag) {
			return fmt.Sprintf("not summarized: the capture is tagged %q, so its text stays on this machine", PrivateTag)
		}
	}
	return ""
}
