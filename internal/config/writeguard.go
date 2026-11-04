package config

import "github.com/monoes/mono-agent/internal/operator"
import "github.com/monoes/mono-agent/internal/storage"

// newConfigWriteGuard is the storage layer's write guard (internal/storage
// cannot import operator): a reserved row (operator.ReservedConfig) is
// refused to any caller notUser names, "" being the user's own terminal.
// The reserved-name test comes first, so an ordinary row never asks notUser
// (which may read the coder-sessions database).
func newConfigWriteGuard(notUser func() string) func(name string) error {
	return func(name string) error {
		if operator.ReservedConfig(name) && notUser() != "" {
			return reservedConfigError(name)
		}
		return nil
	}
}

// Every binary that links the config store gets the guard, so a write that
// skips the commands made for the reserved rows is still refused. It asks
// operator.NotUser: the marker for every process, and, in a process that
// registered the turn-aware gate (cmd/monoagentcli: opgate.Register), also a
// coder turn that cleared its markers, an agent-token holder and a process
// that cannot be placed (all refused). The Wails app, the daemon, MCP
// servers and library use register no gate and are judged by the marker
// alone; that is deliberate and holds them to what they were.
func init() { storage.ConfigWriteGuard = newConfigWriteGuard(operator.NotUser) }
