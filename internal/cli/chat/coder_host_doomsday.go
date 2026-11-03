package chat

import (
	"errors"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// RunHooks are what an open-ended mode installs into the real coder host's launchers. All fields are optional; a launcher without them
// launches exactly as it always did.
type RunHooks struct {
	// RunStamp says which run (charter id, generation) a session's turn belongs to; "" and 0 stamp nothing.
	RunStamp func(profileID, conversationID string) (charterID string, generation int64)
	// BeforeRelease is asked before anything is delivered to the turn's process; bound says whether the turn is peer-credential bound.
	BeforeRelease func(profileID, conversationID, turnID string, bound bool) error
}

// InstallRunHooks sets the hooks on the launchers of a host built by `coder host run` (the legacy and the bound one) and wires the
// stamp to the store. It fails when the host does not launch through the production launchers.
func InstallRunHooks(st *coderhost.Store, h *coderhost.Host, hooks RunHooks) error {
	g, ok := h.L.(*gatedLauncher)
	if !ok {
		return errors.New("the host does not launch through the production gate")
	}
	n := 0
	for _, l := range []coderhost.Launcher{g.inner, g.bound} {
		if e, ok := l.(*execLauncher); ok {
			e.runStamp, e.beforeRelease = hooks.RunStamp, hooks.BeforeRelease
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
