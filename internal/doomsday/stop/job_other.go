//go:build !windows

package stop

// realJobOS is nil off Windows: the Job Object mechanism refuses there.
func realJobOS() JobOS { return nil }
