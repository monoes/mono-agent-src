//go:build !windows

package extension

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func slowCLI(t *testing.T, marker string) *cliChatBackend {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "monoagentcli")
	script := "#!/bin/sh\ncat >/dev/null\nsleep 1\ntouch '" + marker + "'\necho '{\"outcome\":\"accepted\",\"delivered\":\"system\",\"handled\":true}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &cliChatBackend{bin: bin}
}

func finishes(t *testing.T, marker string) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(end) {
			t.Fatal("the command was ended before it finished")
		}
	}
}

// An approval that performs a send can outlast every deadline of the bridge:
// none of them ends it, and the panel is told it is still in progress.
func TestASlowApprovalSurvivesTheBridgeDeadlines(t *testing.T) {
	press := []byte(`{"version":"1.0","action":{"name":"hil.approve","surfaceId":"send-1","sourceComponentId":"btn-approve","context":{"id":"1"}}}`)
	for name, c := range map[string]struct{ wait, deadline time.Duration }{
		"the bridge stops waiting": {100 * time.Millisecond, time.Minute},
		"the request's deadline":   {time.Minute, 100 * time.Millisecond},
	} {
		old := uiPressWait
		uiPressWait = c.wait
		marker := filepath.Join(t.TempDir(), "sent")
		ctx, cancel := context.WithTimeout(context.Background(), c.deadline)
		out, err := slowCLI(t, marker).UIAction(ctx, "work", "c1", press)
		cancel()
		uiPressWait = old
		if err != nil || string(out) != `{"outcome":"accepted","delivered":"system","inProgress":true}` {
			t.Fatalf("%s: %s %v", name, out, err)
		}
		finishes(t, marker)
	}
}

func TestAnAgentPressPastTheDeadlineIsATimeoutAndStillRuns(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "pressed")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := slowCLI(t, marker).UIAction(ctx, "work", "c1", []byte(`{"version":"1.0","action":{"name":"submit_form","surfaceId":"s","sourceComponentId":"b","context":{}}}`))
	var re *RequestError
	if !errors.As(err, &re) || re.Code != CodeTimeout {
		t.Fatalf("%v", err)
	}
	finishes(t, marker)
}

func TestAQuickPressReturnsTheCLIsAnswer(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "done")
	out, err := slowCLI(t, marker).UIAction(context.Background(), "work", "c1", []byte(`{"version":"1.0","action":{"name":"hil.reject"}}`))
	if err != nil || string(out) != `{"outcome":"accepted","delivered":"system","handled":true}` {
		t.Fatalf("%s %v", out, err)
	}
}
