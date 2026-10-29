package ai

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// deleteCallers names every non-test file that deletes a conversation through the store, with why it may.
// `chat history delete` stops the processes the conversation's turns left running, revokes the mission tokens,
// stops a mission it leads, asks the operator, and cleans up the files: none of that happens for a caller of the
// store's delete. A new caller has to be added here on purpose, with its reason, so that it is reviewed against
// that list; the count means a new call in an allowlisted file fails too.
var deleteCallers = map[string]struct {
	why string
	max int
}{
	"internal/cli/chat/chat_history.go":   {"`chat history delete`: the one that stops the processes, asks the operator and revokes the mission tokens", 1},
	"internal/coderhost/spawn_cleanup.go": {"undo of a spawn that could not start: a conversation no row but this reservation names, deleted only while no other row owns it (DeleteConversationIfUnowned); it never ran a turn, so it left no process or mission", 1},
}

var deleteCall = regexp.MustCompile(`\.(DeleteConversation|DeleteConversationGuarded|DeleteConversationIfUnowned)\(`)

// No file outside deleteCallers deletes a conversation through the store, and none does it more often than its entry allows.
func TestOnlyKnownCallersDeleteAConversation(t *testing.T) {
	root := filepath.Join("..", "..")
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "frontend", "chrome-extension", "wailsjs":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "internal/ai/chat_events_conversations.go" {
			return nil // the definitions
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n := len(deleteCall.FindAllString(string(b), -1))
		if n == 0 {
			return nil
		}
		seen[rel] = true
		entry, ok := deleteCallers[rel]
		switch {
		case !ok:
			t.Errorf("%s deletes a conversation through the store (%d call(s)) and is not in deleteCallers: it would skip stopping the processes, the operator check and the token revokes of `chat history delete`; justify it there", rel, n)
		case n > entry.max:
			t.Errorf("%s makes %d store delete call(s), deleteCallers allows %d: a new call needs the same review", rel, n, entry.max)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel := range deleteCallers {
		if !seen[rel] {
			t.Errorf("%s is listed in deleteCallers but no longer deletes a conversation: remove the entry", rel)
		}
	}
}
