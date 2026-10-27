package config

import "github.com/monoes/mono-agent/internal/operator"
import "github.com/monoes/mono-agent/internal/storage"

// newConfigWriteGuard is the storage layer's write guard (internal/storage
// cannot import operator): a reserved row (operator.ReservedConfig) is
// refused to any caller marker names, "" being the operator.
func newConfigWriteGuard(marker func() string) func(name string) error {
	return func(name string) error {
		if operator.ReservedConfig(name) && marker() != "" {
			return reservedConfigError(name)
		}
		return nil
	}
}

// Every binary that links the config store gets the guard, so a write that
// skips the commands made for the reserved rows is still refused.
func init() { storage.ConfigWriteGuard = newConfigWriteGuard(operator.Marker) }
