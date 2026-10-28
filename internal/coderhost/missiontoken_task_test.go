package coderhost

import (
	"strings"
	"testing"
	"time"
)

// The pipe that carries a mission confirmation also carries a child's task token, and nothing else.
func TestThePipeAcceptsAConfirmationAndATaskTokenOnly(t *testing.T) {
	for tok, ok := range map[string]bool{
		"12.abcdef":                     true,
		TaskTokenPrefix + "AbC-_123xyz": true,
		"mat":                           false,
		"matt-12":                       false,
		"plain-text":                    false,
		"has space":                     false,
		TaskTokenPrefix + strings.Repeat("a", 300): false,
	} {
		got, err := readMissionToken(strings.NewReader(tok+"\n"), time.Second)
		if (err == nil) != ok || (ok && got != tok) {
			t.Errorf("%q: got %q, %v; want accepted=%v", tok, got, err, ok)
		}
	}
}
