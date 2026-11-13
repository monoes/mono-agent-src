package doomsday

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// ProbeResult is the answer of `doomsday probe`: whether a person can start an open-ended run from here, and when not,
// why. The reason is a plain sentence; a failed containment probe names its step in it ("containment unavailable:
// <step>: <what failed>").
type ProbeResult struct {
	ModeAvailable bool   `json:"mode_available"`
	Reason        string `json:"reason,omitempty"`
	RunExists     bool   `json:"run_exists"`
	Platform      string `json:"platform"`
	// NotDefended is the plain statement of what the mode does not defend against on this machine; the entry screen
	// shows it before a charter exists.
	NotDefended string `json:"not_defended"`
	// Warning is "running without peer-credential binding: <reason>" when a run started here would go unbound.
	Warning string `json:"warning,omitempty"`
}

// Availability asks the engine, in this order: the profile's run (one run at a time), the containment probe (the real
// host steps of this machine, which also build the witness), then peer-credential binding. The first that fails is the
// reason; nothing is written. The containment probe does not run while a run exists.
func Availability(ctx context.Context, e Engine) ProbeResult {
	r := ProbeResult{Platform: runtime.GOOS, NotDefended: notDefendedText()}
	st, err := e.Status(ctx)
	switch {
	case err != nil:
		r.Reason = "the profile's open-ended run cannot be read: " + err.Error()
	case st.Active:
		r.RunExists, r.Reason = true, fmt.Sprintf("an open-ended run (%s) is already active in this profile: only one runs at a time, stop it first", st.RunID)
	default:
		if err := e.Containment(); err != nil {
			r.Reason = err.Error()
		} else if b, ok := e.(BindingRequirer); ok {
			if err := b.RequireBinding(ctx); err != nil {
				r.Reason = err.Error()
			}
		}
	}
	r.ModeAvailable = r.Reason == ""
	if w, ok := e.(UnboundWarner); ok && r.ModeAvailable {
		r.Warning = w.UnboundWarning(ctx)
	}
	return r
}

// notDefendedText is the "does not defend against" part of the render, whatever platform text it carries.
func notDefendedText() string {
	out := Render(Charter{}, "")
	if i := strings.Index(out, "What this mode does NOT defend against"); i >= 0 {
		return out[i:]
	}
	return out
}

func (d Deps) newProbe() *cobra.Command {
	return &cobra.Command{
		Use:   "probe",
		Short: "Say whether an open-ended run can start here, and if not why (writes nothing)",
		Long: "Says whether an open-ended run can start from here: no run in this profile, the containment probe of this machine, " +
			"peer-credential binding. It runs the real containment steps (a transient test unit on Linux). " + clicore.OperatorOnlyHelp,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := d.operatorGate("probe the open-ended mode"); err != nil {
				return err
			}
			r := Availability(cmd.Context(), d.engine())
			out := cmd.OutOrStdout()
			if d.json() {
				return json.NewEncoder(out).Encode(r)
			}
			if r.ModeAvailable {
				fmt.Fprintf(out, "available on %s\n", r.Platform)
			} else {
				fmt.Fprintf(out, "not available on %s: %s\n", r.Platform, r.Reason)
			}
			return nil
		},
	}
}
