package main

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// ─── A2UI surfaces in chat ──────────────────────────────────────────────────
//
// What a pressed surface button does, and which pending approvals have a card,
// is decided by the CLI (`chat ui action`, `chat ui sync-hil`). These bindings
// only run those commands under the app's explicit profile and relay their
// JSON. The envelope goes over stdin, so the person's edits are in no process
// listing.

// ChatUIAction runs `chat ui action --action - -- CONVERSATION` with the envelope on stdin and returns
// its answer: {outcome, reason?, delivered?, message?, ...}. A press the CLI
// refuses comes back as an outcome of rejected or stale, not as an error.
func (a *App) ChatUIAction(conversationID, actionJSON string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	out, err := a.pressCLI(actionJSON, "chat", "ui", "action", "--action", "-", "--", conversationID)
	if err != nil {
		return aiError(err)
	}
	return out
}

// pressCLI is jsonCLIStdin for a button press, which may perform an approved
// send that takes minutes: like HilSendResolve it has no timeout, and the
// command runs on its own (clicore.RunSurviving), so neither a deadline nor a
// closed window ends it half way.
func (a *App) pressCLI(stdin string, args ...string) (string, error) {
	if a.chatSup == nil {
		return "", fmt.Errorf("chat supervisor not initialized")
	}
	bin, err := a.chatSup.findCLI()
	if err != nil {
		return "", err
	}
	full, err := a.scopedArgs(args...)
	if err != nil {
		return "", err
	}
	cmd := exec.Command(bin, full...) //nolint:gosec // our own CLI
	out, stderr, err := clicore.RunSurviving(context.Background(), cmd, []byte(stdin))
	return cliAnswer(args, out, stderr, err)
}

// ChatUISyncHIL runs `chat ui sync-hil -- CONVERSATION` and returns
// {"surfaced":[ids],"resolved":[ids]}. The cards themselves are journaled as
// turns of their own, which the chat view reads back; they are not streamed.
func (a *App) ChatUISyncHIL(conversationID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("chat", "ui", "sync-hil", "--", conversationID)
}
