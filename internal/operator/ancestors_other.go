//go:build !linux

package operator

// ancestorMarker reads no parent processes outside Linux (no /proc): only a
// process's own environment marks it there.
func ancestorMarker() string { return "" }
