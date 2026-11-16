package chat

import (
	"strings"
	"testing"
)

func TestChatBlocksPromptNamesEveryBlockType(t *testing.T) {
	p := chatBlocksPrompt()
	for _, typ := range []string{"choices", "chart", "table", "card", "list", "steps", "form", "monoagent://"} {
		if !strings.Contains(p, typ) {
			t.Errorf("chatBlocksPrompt does not mention %q", typ)
		}
	}
	if strings.Contains(p, "html-view") {
		t.Error("chatBlocksPrompt must not mention html-view")
	}
}

func TestChatBlocksPromptStaysCompact(t *testing.T) {
	// ~350 tokens at ~4 characters per token.
	if n := len(chatBlocksPrompt()); n > 1600 {
		t.Errorf("chatBlocksPrompt is %d chars, cap is 1600", n)
	}
}

func TestCoderLeadPromptCarriesChatBlocks(t *testing.T) {
	if !strings.Contains(coderSystemPrompt("/tmp/x"), chatBlocksPrompt()) {
		t.Error("coderSystemPrompt lacks the chat blocks guidance")
	}
}

func TestChatBlocksPromptPutsTableLinkOnTheRow(t *testing.T) {
	p := chatBlocksPrompt()
	if !strings.Contains(p, `"rows":[{"name":"A","link":"monoagent://`) || !strings.Contains(p, "never as a column") {
		t.Error("table example must show link on the row object, not as a column")
	}
}
