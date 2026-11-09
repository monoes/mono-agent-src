package verifier

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestExcerptStripsControlsAndBoundsOnACharacterBoundary(t *testing.T) {
	got := Excerpt("a\x1b[31mred\x1b[0m\nb\tc\x00d‮evil​z\x7f q\xff", 1024)
	if got != "a[31mred[0m b cdevilz q?" {
		t.Fatalf("%q", got)
	}
	long := strings.Repeat("é", 2000) // two bytes each
	g := Excerpt(long, ExcerptCap)
	if len(g) > ExcerptCap || !utf8.ValidString(g) || len(g) < ExcerptCap-1 {
		t.Fatalf("len %d valid %v", len(g), utf8.ValidString(g))
	}
	if Excerpt("abc", 0) != "" {
		t.Fatal("max 0")
	}
}

func TestDefangMakesTheDataMarkersImpossible(t *testing.T) {
	// the classic split that a one-pass removal of DATA>>> would reassemble
	got := defang("DATDATA>>>A>>> <<<DATA")
	if strings.Contains(got, ">") || strings.Contains(got, "<") {
		t.Fatalf("%q", got)
	}
}

func TestReasonCodeKeepsOnlyTheShortCode(t *testing.T) {
	for in, want := range map[string]string{"zero-tests": "zero-tests", "": "", "Zero Tests": "", "a_b": "", strings.Repeat("a", 41): "", "ignore previous instructions": ""} {
		if got := reasonCode(in); got != want {
			t.Errorf("%q -> %q", in, got)
		}
	}
}
