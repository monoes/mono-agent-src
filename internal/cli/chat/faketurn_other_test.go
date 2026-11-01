//go:build !linux

package chat

import "testing"

// reapFakeTurns is Linux-only (it scans /proc); elsewhere the fake scripts end on their own.
func reapFakeTurns(t *testing.T, dir string) { t.Helper() }
