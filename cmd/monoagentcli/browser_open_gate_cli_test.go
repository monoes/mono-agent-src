package main

import (
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// The sign-in command's real wiring (account_adapter.go) asks the turn-aware gate (#1424): a refused caller
// opens nothing, is not told a browser is opening, and is not left waiting out the sign-in timeout.
func TestAccountLoginRefusedByTheGateEndsAtOnce(t *testing.T) {
	f := newLibFixture(t)
	var opened []string
	prev := openLoginURL
	openLoginURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openLoginURL = prev })
	why := "this process runs inside a coder turn"
	opgate.SetForTest(t, func(bool) string { return why })

	start := time.Now()
	_, errOut, err := runCLI(t, f.home, "account", "login", "--timeout", "4s")
	elapsed := time.Since(start)
	if len(opened) != 0 {
		t.Errorf("the refused sign-in opened %v", opened)
	}
	if err == nil || !strings.Contains(err.Error(), why) || !strings.Contains(err.Error(), "yourself in your terminal") {
		t.Errorf("err %v; want the refusal and where to run the sign-in", err)
	}
	if strings.Contains(errOut, "Opening your browser") {
		t.Errorf("a refused sign-in said it was opening the browser:\n%s", errOut)
	}
	if elapsed > 2*time.Second {
		t.Errorf("a refused sign-in took %v; want it to end at once", elapsed)
	}
}
