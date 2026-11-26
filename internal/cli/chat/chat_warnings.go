package chat

import (
	"fmt"
	"io"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// unsandboxed is the warning ev calls for: non-empty for the start event of a
// turn that runs without a sandbox, whatever the fallback setting was.
func unsandboxed(runtime string, ev monomind.Event) string {
	if ev.Type != monomind.EventStart || !monomind.SandboxMissing(ev.SandboxStatus) {
		return ""
	}
	return monomind.UnsandboxedWarning(runtime, ev.SandboxStatus)
}

// warnUnsandboxed wraps a plain turn's event handler with the stderr warning.
func warnUnsandboxed(w io.Writer, runtime string, next func(monomind.Event)) func(monomind.Event) {
	return func(ev monomind.Event) {
		if msg := unsandboxed(runtime, ev); msg != "" {
			fmt.Fprintln(w, "warning: "+msg)
		}
		next(ev)
	}
}

// sandboxSeverity is the severity of a journaled turn's agent.sandbox notice:
// "warning" (said on stderr too) when the turn runs without a sandbox, which
// the app shows as a warning on the turn, else "info".
func (j *turnJournal) sandboxSeverity(ev monomind.Event) chatevents.NoticeSeverity {
	msg := unsandboxed(j.runtimeID, ev)
	if msg == "" {
		return chatevents.SeverityInfo
	}
	fmt.Fprintln(j.warnTo, "warning: "+msg)
	return chatevents.SeverityWarning
}

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
