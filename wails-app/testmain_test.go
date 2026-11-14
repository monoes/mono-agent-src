package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent.
func TestMain(m *testing.M) {
	if fakeCLIShimMain() { // Windows: this binary is standing in for monoagentcli
		return
	}
	testhome.Main(m)
}
