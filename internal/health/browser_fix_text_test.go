package health

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/chromium"
)

// The by-hand extension fix names the folder the extension is really
// unpacked to, and an extensions page for every known Chromium browser.
func TestExtensionInstallFix_NamesTheFolderAndEveryBrowser(t *testing.T) {
	var cmd string
	for _, f := range browserFixes() {
		if f.ID == FixExtensionInstall {
			cmd = f.Command
		}
	}
	if cmd == "" {
		t.Fatalf("no %s fix", FixExtensionInstall)
	}
	if !strings.Contains(cmd, "~/.monoagent/extension") {
		t.Errorf("fix command %q does not name ~/.monoagent/extension", cmd)
	}
	if strings.Contains(cmd, "chrome-extension folder") {
		t.Errorf("fix command %q still names the chrome-extension folder", cmd)
	}
	for _, b := range chromium.Known() {
		if !strings.Contains(cmd, b.ExtensionsURL) {
			t.Errorf("fix command %q does not name %s's %s", cmd, b.Name, b.ExtensionsURL)
		}
	}
}
