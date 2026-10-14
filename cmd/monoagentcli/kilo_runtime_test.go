package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

func TestKiloCoderReadinessUsesCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		installed, full, modern, ready bool
	}{
		{"no full access", true, false, true, false},
		{"not installed", false, true, true, false},
		{"old monomind", true, true, false, false},
		{"future supported runner", true, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := newChatCLITestDB(t)
			caps := monomind.CoderCapabilities
			if tc.modern {
				caps = allRuntimeCaps
			}
			withCoderCaps(t, caps...)
			withCoderScan(t, monomind.ScanEntry{ID: "kilo", Installed: tc.installed, FullAccess: tc.full})
			out, code := runCoderCLI(t, dbPath, "status")
			if code != 0 {
				t.Fatalf("status: exit %d %s", code, out)
			}
			var got struct {
				Runtimes []struct {
					ID         string `json:"id"`
					Ready      bool   `json:"ready"`
					Resume     bool   `json:"resume"`
					InitTarget string `json:"initTarget"`
				} `json:"runtimes"`
			}
			decodeChatJSON(t, out, &got)
			if len(got.Runtimes) != 1 || got.Runtimes[0].ID != "kilo" || got.Runtimes[0].Ready != tc.ready || got.Runtimes[0].Resume || got.Runtimes[0].InitTarget != "" {
				t.Fatalf("capabilities invented or ignored: %+v", got)
			}
		})
	}
}
