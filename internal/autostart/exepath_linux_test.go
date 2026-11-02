package autostart

import (
	"os"
	"path/filepath"
	"testing"
)

// The exe of a unit is read back exactly as renderUnit wrote it, whatever the path holds.
func TestRegisteredExeReadsBackWhatRenderUnitWrote(t *testing.T) {
	for _, exe := range []string{
		"/home/u/.local/bin/monoagentcli",
		`/home/u/My Apps/mono "agent"/100%/$x/monoagentcli`,
		`/opt/back\slash/monoagentcli`,
	} {
		unit, err := renderUnit(exe)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(t.TempDir(), "monoagent-daemon.service")
		if err := os.WriteFile(p, []byte(unit), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, ok := RegisteredExe(p); !ok || got != exe {
			t.Errorf("RegisteredExe = %q, %v, want %q", got, ok, exe)
		}
	}
}
