package chat

import (
	"fmt"
	"io"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// The warnings a chat prints on stderr, never stdout: a --json result and a
// turn's event stream stay parseable.

// fullAccessWarning is said when a chat is created at full access and when one
// starts its first turn there, whether the level was named or the default.
const fullAccessWarning = "warning: this chat has full access: it can run any command and change any file as you (--access scoped is the sandboxed assistant)"

// unenforcedAccessWarning is said instead for a coding or workspace chat while
// coder.box is off; %s is the level.
const unenforcedAccessWarning = "warning: the %s access level is not enforced because the coder box is off: this chat can run any command and change any file as you (--access scoped is the sandboxed assistant)\n"

// warnAccess says what a new chat can really do: everything at full access
// ("" is the plain `chat --mode coder` turn, which is full), and also at coding
// or workspace while coder.box is off, since such a turn runs with monomind's
// full access and nothing around it (runCoderTurn, coderBox).
func warnAccess(w io.Writer, access string, s coderSettings) {
	switch {
	case access == "" || access == ai.AccessFull:
		fmt.Fprintln(w, fullAccessWarning)
	case access != ai.AccessScoped && s.boxMode() == BoxOff:
		fmt.Fprintf(w, unenforcedAccessWarning, access)
	}
}

// warnNewChatAccess is warnAccess for a chat just created. Settings that cannot
// be read count as box off, so the warning is not lost.
func warnNewChatAccess(w io.Writer, cfg *Deps, access string) {
	var s coderSettings
	if db, err := cfg.OpenDB(); err == nil {
		if stored, err := loadCoderSettings(db.DB); err == nil {
			s = stored
		}
		db.Close()
	}
	warnAccess(w, access, s)
}

// startsChat reports a turn that starts a chat. A journaled chat starts with
// its only turn (turns that cannot be read count as the first); a plain turn
// starts one unless it resumes.
func startsChat(j *turnJournal, resume string) bool {
	if j == nil {
		return resume == ""
	}
	turns, _, err := j.store.ListTurns(j.conversationID, j.profileID, "", 2)
	return err != nil || len(turns) <= 1
}

// sandboxSeverity is the severity of a journaled turn's agent.sandbox notice:
// "warning" when the turn runs without a sandbox, which the app shows as a
// warning on the turn (monomind.Exec says it on stderr), else "info".
func sandboxSeverity(status string) chatevents.NoticeSeverity {
	if monomind.SandboxMissing(status) {
		return chatevents.SeverityWarning
	}
	return chatevents.SeverityInfo
}

// warnUnconfinedWorker is the stderr line for a dynamic org's research worker
// that starts without its confinement: no read access and no sandbox, so it
// runs with full access, only told to stay read-only. Exec is asked for no
// sandbox on that turn, so nothing else says it. Once per run: as it turns to
// working.
func warnUnconfinedWorker(w io.Writer, typ chatevents.EventType, payload any) {
	p, ok := payload.(chatevents.AgentStatusPayload)
	if !ok || typ != chatevents.EventAgentStatus || p.From != chatevents.AgentStarting || p.To != chatevents.AgentWorking || p.Confinement != chatevents.ConfinementWriteLease {
		return
	}
	fmt.Fprintf(w, "warning: worker %s is running without a sandbox: its runtime cannot confine it to reading, so it has full access\n", p.AgentID)
}
