//go:build !windows

package coderhost

import "testing"

// looseEntry: the mode given to writeEntry already made the entry loose.
func looseEntry(*testing.T, string) {}
