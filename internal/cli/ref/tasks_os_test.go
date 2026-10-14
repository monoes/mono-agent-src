package ref

import (
	"strings"
	"testing"
)

func TestRefTasksSaysHowToCaptureFromAnyApp(t *testing.T) {
	t.Parallel()
	for _, want := range []string{
		"FROM ANY APP ON A MAC", "task os install", "task os status", "task os uninstall",
		"task os install and task os uninstall are the operator's", "Keyboard Shortcuts, Services, Text",
		"task add --stdin --source os", "xclip", "wl-paste", "Get-Clipboard",
	} {
		if !strings.Contains(TasksText, want) {
			t.Errorf("`ref tasks` does not mention %q", want)
		}
	}
}
