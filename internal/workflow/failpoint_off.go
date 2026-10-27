//go:build !wfcrash || windows

package workflow

// failpoint marks a point of the publication protocol where crash tests can stop a process or
// inject an error. Release builds have none: this is a no-op.
func failpoint(string) error { return nil }
