//go:build darwin

package stop

// realSessionOS is the macOS binding (sessunit_darwin.go), the macOS run unit (platformKinds["darwin"]).
func realSessionOS() SessionOS { return darwinSess{} }
