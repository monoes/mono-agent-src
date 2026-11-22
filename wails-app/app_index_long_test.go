package main

import (
	"runtime"
	"testing"
	"time"
)

func TestIndexProfileDocumentOutlastsTheDefaultCLITimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script CLI")
	}
	t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "sleep 1\necho '{}'\n"))
	old := defaultCLITimeout
	defaultCLITimeout = 200 * time.Millisecond
	defer func() { defaultCLITimeout = old }()
	a := newTestApp(t)
	a.selectForTest("work")
	if _, err := a.IndexProfileDocument("doc-001"); err != nil {
		t.Fatalf("indexing a document was cut off: %v", err)
	}
}
