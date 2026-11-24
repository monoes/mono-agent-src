package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A login subprocess that never exits is killed at oauthLoginTimeout and the
// page is told the login failed.
func TestSocialLoginSubprocessesAreBounded(t *testing.T) {
	for name, start := range map[string]func(a *App) string{
		"LoginSocial":        func(a *App) string { return a.LoginSocial("instagram") },
		"ConfirmSocialLogin": func(a *App) string { return a.ConfirmSocialLogin("instagram") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MONOAGENTCLI_BIN", fakeCLI(t, "exec sleep 60\n"))
			orig := oauthLoginTimeout
			oauthLoginTimeout = 300 * time.Millisecond
			t.Cleanup(func() { oauthLoginTimeout = orig })

			done := make(chan map[string]interface{}, 4)
			var mu sync.Mutex
			origEmit := eventsEmit
			eventsEmit = func(_ context.Context, ev string, data ...interface{}) {
				mu.Lock()
				defer mu.Unlock()
				if ev == "conn:done" {
					done <- data[0].(map[string]interface{})
				}
			}
			t.Cleanup(func() { eventsEmit = origEmit })

			a := newTestApp(t)
			a.ctx = context.Background()
			a.selectForTest("work")
			if got := start(a); got != "started" {
				t.Fatalf("start = %q", got)
			}
			select {
			case d := <-done:
				if d["success"] != false {
					t.Fatalf("conn:done = %v", d)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the login subprocess outlived its timeout")
			}
		})
	}
}
