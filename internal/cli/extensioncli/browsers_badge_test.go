package extensioncli

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/extension"
)

// An unrestricted paired browser is flagged in the listing; a restricted one is not (r28-R28-3).
func TestBrowsersListBadgesABrowserThatCanClaimAnyProfile(t *testing.T) {
	pairTestHome(t)
	home, _ := os.UserHomeDir()
	seedPaired(t, home,
		extension.PairedBrowser{ID: "aaaa1111bbbb2222", Label: "Open", PairedAt: time.Now()},
		extension.PairedBrowser{ID: "cccc3333dddd4444", Label: "Tied", PairedAt: time.Now(), Restricted: true, AllowedProfiles: []string{"p-work"}})
	out, err := runExt(t, allowDeps(t), "browsers", "list")
	if err != nil {
		t.Fatal(err)
	}
	var open, tied string
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(l, "aaaa1111"):
			open = l
		case strings.Contains(l, "cccc3333"):
			tied = l
		}
	}
	if !strings.Contains(open, "can claim any profile") {
		t.Errorf("unrestricted row has no badge: %q", open)
	}
	if strings.Contains(tied, "can claim any profile") || !strings.Contains(tied, "p-work") {
		t.Errorf("restricted row: %q", tied)
	}
}
