package clicore

import "testing"

func TestTruncateStrIsRuneSafeAndClamped(t *testing.T) {
	if got := TruncateStr("héllo wörld", 8); got != "héllo..." {
		t.Errorf("multibyte: %q", got)
	}
	if got := TruncateStr("日本語日本語日本語", 5); got != "日本..." {
		t.Errorf("cjk: %q", got)
	}
	if got := TruncateStr("abcdef", -2); got != "" {
		t.Errorf("negative n: %q", got)
	}
	if got := TruncateStr("abc", 3); got != "abc" {
		t.Errorf("fits: %q", got)
	}
}
