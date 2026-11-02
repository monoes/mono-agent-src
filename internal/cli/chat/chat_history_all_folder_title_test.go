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

// Invisible characters are dropped before the redaction on every derived source, and a source that is
// literally "New chat" is a title, not the placeholder.
func TestDerivedTitlesAreNormalisedBeforeRedactionAndTheFallbackIsExplicit(t *testing.T) {
	sneaky := "pass​word: hunter2xyz"
	for name, got := range map[string]string{
		"goal":   historyTitle(sneaky, "", ""),
		"prompt": historyTitle("", sneaky, ""),
		"folder": historyTitle("", "", "/tmp/pass​word=hunter2xyz"),
	} {
		if strings.Contains(got, "hunter2xyz") || strings.Contains(coderhost.AliasFromTitle(got), "hunter2xyz") {
			t.Errorf("%s title carries the secret: %q", name, got)
		}
	}
	if _, fb := historyTitleSource("New chat", "", ""); fb {
		t.Error("a goal called New chat was taken for the placeholder")
	}
	if _, fb := historyTitleSource("", "", ""); !fb {
		t.Error("nothing to title the chat by is not the placeholder")
	}
}
