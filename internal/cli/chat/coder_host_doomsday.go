package chat

import (
	"context"
	"errors"
	"os/exec"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// RunHooks are what an open-ended mode installs into the real coder host's launchers. All fields are optional; a launcher without them
// launches exactly as it always did.
type RunHooks struct {
	// RunStamp says which run (charter id, generation) a session's turn belongs to; "" and 0 stamp nothing.
	RunStamp func(profileID, conversationID string) (charterID string, generation int64)
	// Place puts the turn's process where it must be born, before it exists (see execLauncher.place); nil launches as before.
	Place func(cmd *exec.Cmd, profileID, conversationID string) (release func(), err error)
	// Adopt stub (red commit).
	Adopt func(profileID, conversationID string, pid int, stamp string) (held bool, err error)
	// BeforeRelease is asked before anything is delivered to the turn's process; bound says whether the turn is peer-credential bound.
	// ctx is cancelled when the launcher gives up on the check (a deadline) or has returned: check it before any effect that cannot be undone.
	BeforeRelease func(ctx context.Context, profileID, conversationID, turnID string, bound bool) error
}

// InstallRunHooks sets the hooks on the launchers of a host built by `coder host run` (the legacy and the bound one) and wires the
// stamp to the store. It fails when the host does not launch through the production launchers.
func InstallRunHooks(st *coderhost.Store, h *coderhost.Host, hooks RunHooks) error {
	if h == nil || (hooks.RunStamp != nil && st == nil) {
		return errors.New("the run hooks need a host, and a store to stamp the run")
	}
	g, ok := h.L.(*gatedLauncher)
	if !ok {
		return errors.New("the host does not launch through the production gate")
	}
	n := 0
	for _, l := range []coderhost.Launcher{g.inner, g.bound} {
		if e, ok := l.(*execLauncher); ok {
			e.runStamp, e.beforeRelease, e.place = hooks.RunStamp, hooks.BeforeRelease, hooks.Place
			if hooks.RunStamp != nil {
				e.recordRun = st.StampDoomsdayRun
			}
			n++
		}
	}
	if n == 0 {
		return errors.New("the host has no production launcher to install into")
	}
	return nil
}
