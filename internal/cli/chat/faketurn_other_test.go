//go:build !linux

package chat

import "testing"

// reapFakeTurns and fakeRecord are Linux-only (they use /proc); elsewhere the fake scripts end on their own.
func reapFakeTurns(t *testing.T, dir string) { t.Helper() }

func fakeRecord(dir string) string { return "" }
