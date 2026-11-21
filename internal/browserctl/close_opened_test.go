package browserctl_test

import (
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/extension"
)

// CloseOpenedTab closes the session's own tab after its page moved to
// another origin, without expectOrigin; any other tab is refused.
func TestCloseOpenedTab(t *testing.T) {
	clearMarkers(t)
	f := browserctltest.New()
	other := f.AddTab("https://user.example/", "", false)
	perms := browserctl.DefaultPermissions()
	s, err := browserctl.Open(browserctl.Options{NoTab: true, Permissions: &perms, Caller: &browserctl.Caller{Kind: browserctl.KindChat}, Sender: f})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.OpenTab("https://example.com/", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CloseOpenedTab(other); err == nil {
		t.Fatalf("closed a tab that is not the session's")
	}
	if _, open := f.Tab(other); !open {
		t.Fatalf("the user's tab was closed")
	}
	f.SetURL(id, "https://elsewhere.example/")
	if err := s.CloseOpenedTab(id); err != nil {
		t.Fatal(err)
	}
	if _, open := f.Tab(id); open {
		t.Fatalf("tab %d still open", id)
	}
	for _, c := range f.Calls() {
		if c.Type == extension.CmdCloseTab && c.Params[browserctl.ExpectOriginParam] != nil {
			t.Errorf("close_tab carried expectOrigin")
		}
	}
	if s.TabID() != 0 {
		t.Errorf("the session still names tab %d", s.TabID())
	}
}
