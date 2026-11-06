//go:build !linux && !windows

package stop

// SystemHost is nil off Linux and Windows: there is no containment unit, so no run can be created (the probe refuses first).
func SystemHost() UnitHost { return nil }
