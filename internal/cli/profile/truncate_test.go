package profile

import (
	"testing"
	"unicode/utf8"
)

// truncateStr cuts on characters, not bytes: a Persian or CJK name is never left as invalid UTF-8.
func TestTruncateStrKeepsValidUTF8(t *testing.T) {
	got := truncateStr("سلام دنیا، خوش آمدید", 10)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateStr produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 10 {
		t.Fatalf("truncateStr = %q (%d chars), want 10", got, n)
	}
}
