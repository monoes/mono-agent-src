package accountcli

import (
	"strings"
	"testing"
)

// A code is a few digits: a stdin that never ends a line is refused, not read into memory.
func TestReadLineIsBounded(t *testing.T) {
	t.Parallel()
	if _, err := readLine(strings.NewReader(strings.Repeat("a", 1<<20))); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("err = %v, want a too-long refusal", err)
	}
	if got, err := readLine(strings.NewReader("123456\n")); err != nil || got != "123456" {
		t.Fatalf("a normal code: %q %v", got, err)
	}
}
