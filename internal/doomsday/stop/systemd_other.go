//go:build !linux

package stop

// SystemHost is nil off Linux: there is no containment unit, so no run can be created (the probe refuses first).
func SystemHost() UnitHost { return nil }
