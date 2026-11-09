//go:build !windows

package procbind

// LockSelf is Windows hardening of the host process's own access list; the other kernels need none.
func LockSelf() error { return nil }
