package monomind

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/monoes/mono-agent/internal/logging"
)

// boxedCommand is the command that runs bin under opts.Box: the canary runs first, in
// the same box configuration, and a box that does not hold starts nothing (the error
// wraps ErrSandboxRequired). The files Exec wrote for the turn and the attachments are
// bound read-only, and the variables the caller set for the turn are passed on.
func boxedCommand(ctx context.Context, opts ExecOptions, bin string, args, env []string) (*exec.Cmd, error) {
	b := *opts.Box
	b.Binaries = append(append([]string(nil), b.Binaries...), bin)
	if self, err := os.Executable(); err == nil {
		b.Binaries = append(b.Binaries, self)
	}
	if b.Runtime == "" {
		b.Runtime = opts.Runtime
	}
	if b.Cwd == "" {
		b.Cwd = opts.Cwd
	}
	b.ReadOnly = append([]string(nil), b.ReadOnly...)
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--prompt-file", "--system-file", "--tools-file":
			b.ReadOnly = append(b.ReadOnly, args[i+1])
		}
	}
	b.Attach = append(append([]string(nil), b.Attach...), opts.Attach...)
	for k := range opts.Env {
		b.PassEnv = append(b.PassEnv, k)
	}
	sort.Strings(b.PassEnv)
	b, err := b.Pin()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSandboxRequired, err)
	}
	if err := b.Canary(ctx); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSandboxRequired, err)
	}
	plan, err := b.Wrap(bin, args, env)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSandboxRequired, err)
	}
	for _, n := range plan.Notes {
		logging.FromContext(ctx).Warn("agent box", "note", n)
	}
	return plan.Command(), nil
}
