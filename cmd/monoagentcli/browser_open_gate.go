package main

import (
	"fmt"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/connections"
)

// Opening a page in the user's browser is the user's own (#1424): an agent's turn, or an unmarked process
// inside one, asks opgate.Refusal exactly as the browser launch of EnsureConnected does. The OAuth flows
// ask it through connections.BrowserRefusal; the monoes.me sign-in asks it in openLoginURLGated.
func init() { connections.BrowserRefusal = opgate.Refusal }

// openLoginURLGated is the sign-in's browser opener: for a non-operator the page is not opened, and the
// sign-in goes on with the URL it already showed, as when no browser can be started.
func openLoginURLGated(u string) error {
	if why := opgate.Refusal(); why != "" {
		return fmt.Errorf("not opened, opening a browser is the user's own: %s", why)
	}
	return openLoginURL(u)
}
