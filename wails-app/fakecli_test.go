package main

import (
	"path/filepath"
	"testing"
)

// fakeCLI writes an executable shell script standing in for monoagentcli.
func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), fakeCLIName)
	if err := writeFakeCLI(p, "#!/bin/sh\n"+body); err != nil {
		t.Fatal(err)
	}
	return p
}
