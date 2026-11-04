package connections

import "fmt"

// BrowserRefusal is why this process may not open a browser window on the user's screen for an OAuth
// authorization, "" for the user's own terminal (the CLI sets it to opgate.Refusal; this package cannot
// import it). Nil asks nothing, which is what library use and the tests of other packages get.
var BrowserRefusal func() string

// refuseBrowserOpen is the error that stops an OAuth flow before it starts a callback server or opens a
// page, when the caller is not the user: an agent's turn must not pop an authorization page open.
func refuseBrowserOpen() error {
	if BrowserRefusal == nil {
		return nil
	}
	if why := BrowserRefusal(); why != "" {
		return fmt.Errorf("opening a browser for an OAuth authorization is the user's own: %s; run `monoagentcli connect` yourself in your terminal, or connect in the app", why)
	}
	return nil
}
