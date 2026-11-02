package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// A folder named like a secret does not title the chat with it, and so does not become its mesh name.
func TestHistoryTitleFromFolderIsRedacted(t *testing.T) {
	title := historyTitle("", "", "/tmp/password=hunter2xyz")
	if strings.Contains(title, "hunter2xyz") {
		t.Fatalf("title carries the secret: %q", title)
	}
	if alias := coderhost.AliasFromTitle(title); strings.Contains(alias, "hunter2xyz") {
		t.Fatalf("alias carries the secret: %q", alias)
	}
	if got := historyTitle("", "", "/work/api"); got != "api" {
		t.Fatalf("plain folder = %q", got)
	}
	if got := historyTitle("", "", "/work/"+strings.Repeat("d", 300)); len([]rune(got)) > histTitleMax {
		t.Fatalf("folder title not bounded: %d", len([]rune(got)))
	}
}
