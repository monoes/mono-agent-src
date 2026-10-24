//go:build !linux

package agentbox

import "context"

// Wrap is not available off Linux.
func (b Box) Wrap(bin string, args, env []string) (Plan, error) {
	return Plan{}, errf(CodeUnsupportedOS, "the agent sandbox needs bubblewrap and runs on Linux only")
}

// Canary is not available off Linux.
func (b Box) Canary(context.Context) error {
	return errf(CodeUnsupportedOS, "the agent sandbox needs bubblewrap and runs on Linux only")
}
