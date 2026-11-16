package main

import (
	"testing"

	"github.com/monoes/mono-agent/internal/extension"
)

func TestBrowserRowsFlagAStaleExtension(t *testing.T) {
	rows := browserRows([]extension.ConnInfo{
		{Instance: "a", Version: "1.4.0"},
		{Instance: "b", Version: extension.MinExtension},
		{Instance: "c"},
	})
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	if !rows[0].Outdated || rows[0].Warning == "" {
		t.Errorf("1.4.0 should be outdated with a warning: %+v", rows[0])
	}
	if rows[1].Outdated || rows[1].Warning != "" {
		t.Errorf("the minimum version is current: %+v", rows[1])
	}
	if !rows[2].Outdated {
		t.Errorf("an extension that sends no version is outdated: %+v", rows[2])
	}
}
