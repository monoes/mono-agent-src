package main

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/connections"
)

// The sign-in's browser opener and the OAuth flows ask the turn-aware gate (#1424): a refused caller opens
// nothing, the operator's open goes through, and the OAuth hook is wired to the same question.
func TestBrowserOpenAsksTheTurnAwareGate(t *testing.T) {
	var opened []string
	prev := openLoginURL
	openLoginURL = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openLoginURL = prev })

	why := "this process runs inside a coder turn"
	opgate.SetForTest(t, func(bool) string { return why })
	err := openLoginURLGated("https://monoes.me/authorize")
	if err == nil || !strings.Contains(err.Error(), why) || len(opened) != 0 {
		t.Errorf("refused: err %v, opened %v; want the refusal and nothing opened", err, opened)
	}
	if connections.BrowserRefusal == nil || connections.BrowserRefusal() != why {
		t.Errorf("connections.BrowserRefusal is not wired to opgate.Refusal")
	}

	why = ""
	if err := openLoginURLGated("https://monoes.me/authorize"); err != nil || len(opened) != 1 {
		t.Errorf("operator: err %v, opened %v; want one open", err, opened)
	}
}
