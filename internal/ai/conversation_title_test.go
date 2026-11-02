package ai

import "testing"

func TestCleanConversationTitle(t *testing.T) {
	cases := map[string]string{
		"  a \t b\n c  ":           "a b c",
		"x\x07y":                   "xy",
		"safe\u202etxt.exe\u200b!": "safetxt.exe!",
		"   ":                      "",
	}
	for in, want := range cases {
		if got := CleanConversationTitle(in); got != want {
			t.Errorf("CleanConversationTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
