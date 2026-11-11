package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

// #1594: the binary links internal/hilscrub, whose init sets the hook that
// scrubs older card rows before a message is read; a binary without it would
// read messages unscrubbed, silently.
func TestTheBinaryLinksTheMessageScrub(t *testing.T) {
	if storage.ReadHook == nil {
		t.Fatal("storage.ReadHook is nil: internal/hilscrub is not linked into monoagentcli")
	}
}
