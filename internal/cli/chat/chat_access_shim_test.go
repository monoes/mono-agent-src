package chat

import "github.com/monoes/mono-agent/internal/ai/chatevents"

// backgroundNotice records pids the way a turn whose own marker is the first pid's would.
func backgroundNotice(pids []int) chatevents.NoticePayload {
	root := ""
	if len(pids) > 0 {
		root = processIdentity(pids[0])
	}
	return backgroundNoticeFor(root, pids)
}
