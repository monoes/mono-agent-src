//go:build !darwin

package stop

// realSessionOS is nil off macOS: the session mechanism refuses there.
func realSessionOS() SessionOS { return nil }
