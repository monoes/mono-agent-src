//go:build darwin

package stop

// realSessionOS is the macOS binding (sessunit_darwin.go). platformKinds still names the process-group unit for macOS: the
// session unit is compiled and answers a probe here, and the host wiring (S3) and the gate (S4) switch it on.
func realSessionOS() SessionOS { return darwinSess{} }
