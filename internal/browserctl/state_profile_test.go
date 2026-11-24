package browserctl_test

import (
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
)

// One browser instance can serve several profiles, so the tab a context last
// used for profile p1 must not be the default tab of the same context under p2.
func TestSessionStateIsPerProfile(t *testing.T) {
	setEnv(t, false)
	f := browserctltest.New()
	active := f.AddTab("file:///home/me/a.html", "", true)
	other := f.AddTab("file:///home/me/b.html", "", false)

	if _, err := browserctl.Open(browserctl.Options{Profile: "p1", Tab: other, Sender: f}); err != nil {
		t.Fatal(err)
	}
	p2, err := browserctl.Open(browserctl.Options{Profile: "p2", Sender: f})
	if err != nil {
		t.Fatal(err)
	}
	if p2.TabID() != active {
		t.Fatalf("profile p2 landed on tab %d, the one profile p1 used last; want the active tab %d", p2.TabID(), active)
	}
	// The same profile again picks up the tab it used last.
	if _, err := browserctl.Open(browserctl.Options{Profile: "p2", Tab: other, Sender: f}); err != nil {
		t.Fatal(err)
	}
	again, err := browserctl.Open(browserctl.Options{Profile: "p2", Sender: f})
	if err != nil {
		t.Fatal(err)
	}
	if again.TabID() != other {
		t.Fatalf("profile p2 forgot its own last tab: got %d, want %d", again.TabID(), other)
	}
}
