package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// The org tools must not advertise what the chat layer always refuses: approval
// none, level full and policy are the user's own to set (R08-06).
func TestOrgToolsDoNotAdvertiseWhatTheChatRefuses(t *testing.T) {
	for _, tl := range monoagentAdaptedTools() {
		var field, mustMention string
		switch tl.name {
		case "org_grant_set":
			field, mustMention = "approval", "user"
		case "org_autonomy_set":
			field, mustMention = "level", "user"
		default:
			continue
		}
		props, _ := tl.schema["properties"].(map[string]interface{})
		raw, _ := json.Marshal(props[field])
		text := string(raw)
		if field == "level" && strings.Contains(text, "full") && !strings.Contains(text, "refused") {
			t.Errorf("%s advertises level full without saying it is refused: %s", tl.name, text)
		}
		if field == "level" && strings.Contains(text, "manual | mid | full") {
			t.Errorf("%s still lists full as a level: %s", tl.name, text)
		}
		if !strings.Contains(text, mustMention) {
			t.Errorf("%s.%s does not say none/full is the user's to set: %s", tl.name, field, text)
		}
		if !strings.Contains(tl.description, "user") || !strings.Contains(tl.description, "refus") {
			t.Errorf("%s description does not say what is refused: %s", tl.name, tl.description)
		}
	}
}
