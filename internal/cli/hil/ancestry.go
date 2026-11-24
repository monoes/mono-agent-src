package hil

import (
	"strings"

	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/operator"
)

// ancestorPrefix starts operator.Marker's answer for a process that only a parent marks.
const ancestorPrefix = "a parent process"

// The node-run gate asks the same ancestor check the operator commands use (requireOperator), so an
// agent that strips its own markers (`env -u`) and runs `node run` still gets its send held. Only the
// ancestor verdict counts here: a workflow run's own marks are judged by hilsend's unapproved-run rules.
func init() {
	hilsend.AncestorCheck = func() string {
		if m := operator.Marker(); strings.HasPrefix(m, ancestorPrefix) {
			return operator.Refusal()
		}
		return ""
	}
}
