package verifier

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Excerpt makes text a program (or an agent) wrote safe to keep and show: invalid UTF-8 becomes "?", whitespace controls
// (line breaks, tabs, form feed, vertical tab, NEL, line and paragraph separators) become one space, every other control or
// format character (the ESC byte, bidirectional overrides, zero-width marks) is dropped. Only the control character goes: the
// printable rest of an escape sequence ("[31m") stays as plain text, which is inert in a display that does not interpret it, and the result is cut at max bytes on a character boundary. It is data to display and to hand to the reviewer as
// data; nothing in it is ever an instruction.
func Excerpt(s string, max int) string {
	s = strings.ToValidUTF8(s, "?")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || r == '\f' || r == '\v' || r == 0x85 || r == 0x2028 || r == 0x2029:
			r = ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			continue
		}
		if b.Len()+utf8.RuneLen(r) > max {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

// defang makes the marker characters of the reviewer's data block impossible to write: text that contained "DATA" followed by
// three angle brackets, however it is split up, cannot close the block.
func defang(s string) string {
	return strings.NewReplacer("<", "‹", ">", "›").Replace(s)
}

// reasonCode keeps the reviewer's reason only when it is the short code asked for (lower case letters, digits, dashes).
func reasonCode(s string) string {
	if len(s) == 0 || len(s) > 40 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return ""
		}
	}
	return s
}
