//go:build !darwin

package stop

// realPGroupOS is nil off macOS: the process-group mechanism refuses there.
func realPGroupOS() PGroupOS { return nil }
