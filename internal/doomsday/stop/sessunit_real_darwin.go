//go:build darwin

package stop

// realSessionOS is nil until slice S2 adds the macOS binding (contain_darwin.go: kern.proc.all and getsid): the session
// unit is compiled on macOS and refuses there, and platformKinds still names the process-group unit.
func realSessionOS() SessionOS { return nil }
