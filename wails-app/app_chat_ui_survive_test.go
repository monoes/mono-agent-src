//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A press may perform a send that takes minutes: no timeout of the app ends
// it, and it runs in a session of its own, so closing the app does not either.
func TestChatUIActionIsNotBoundByTheAppsTimeoutOrProcessGroup(t *testing.T) {
	old := chatCLITimeout
	chatCLITimeout = 100 * time.Millisecond
	defer func() { chatCLITimeout = old }()
	pid := filepath.Join(t.TempDir(), "pid")
	group := filepath.Join(t.TempDir(), "group")
	bin := fakeCLI(t, `echo $$ > '`+pid+`'`+"\n"+`cut -d' ' -f5 /proc/$$/stat > '`+group+`' 2>/dev/null || ps -o pgid= $$ > '`+group+`'`+"\n"+
		`sleep 1`+"\n"+`echo '{"outcome":"accepted","delivered":"system"}'`+"\n")
	a, _ := newCLIChatApp(t, bin)
	out := a.ChatUIAction("c1", `{"version":"1.0","action":{"name":"hil.approve","surfaceId":"send-1","sourceComponentId":"b","context":{}}}`)
	if !strings.Contains(out, `"accepted"`) {
		t.Fatalf("the press was cut short: %s", out)
	}
	raw, err := os.ReadFile(group)
	if err != nil {
		t.Fatal(err)
	}
	pgid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pgid == syscall.Getpgrp() {
		t.Fatalf("the command runs in the app's process group: %q", raw)
	}
}
