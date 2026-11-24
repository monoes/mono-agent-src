package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GetProfileDocumentText reads into memory, so it carries the same 25 MB cap
// as GetProfileDocumentData.
func TestGetProfileDocumentTextRefusesOversizedFiles(t *testing.T) {
	big := filepath.Join(t.TempDir(), "big.txt")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxInlinePreviewBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, `echo '{"id":"d1","path":"`+big+`"}'`+"\n"))
	a := newTestApp(t)
	a.ctx = context.Background()
	a.selectForTest("work")
	_, err = a.GetProfileDocumentText("d1")
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("want a too-large error, got %v", err)
	}
}
