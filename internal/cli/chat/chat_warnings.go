package chat

import (
	"fmt"
	"io"

	"github.com/monoes/mono-agent/internal/ai"
)

// The warnings a chat prints on stderr, never stdout: a --json result and a
// turn's event stream stay parseable.

// fullAccessWarning is said when a chat is created at full access and when one
// starts its first turn there, whether the level was named or the default.
const fullAccessWarning = "warning: this chat has full access: it can run any command and change any file as you (--access scoped is the sandboxed assistant)"

func warnFullAccess(w io.Writer) { fmt.Fprintln(w, fullAccessWarning) }

// startsFullChat reports a turn that starts a chat at full access. access ""
// is the plain `chat --mode coder` turn, which runs at full. A journaled chat
// starts with its only turn (turns that cannot be read count as the first, so
// the warning is not lost); a plain turn starts one unless it resumes.
func startsFullChat(j *turnJournal, access, resume string) bool {
	if access != "" && access != ai.AccessFull {
		return false
	}
	if j == nil {
		return resume == ""
	}
	turns, _, err := j.store.ListTurns(j.conversationID, j.profileID, "", 2)
	return err != nil || len(turns) <= 1
}
