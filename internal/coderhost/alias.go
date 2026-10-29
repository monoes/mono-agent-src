package coderhost

import "strings"

// AliasFromTitle is a chat's name as a mesh alias: spaces become dashes, anything the hub would not
// take is dropped, repeats collapse, and the result starts with a letter or digit and is at most 64
// characters. "" when nothing usable is left (the caller falls back to the project-and-time name).
func AliasFromTitle(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range title {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '.' || r == '_':
			b.WriteRune(r)
			dash = false
		case r == '-' || r == ' ' || r == '\t':
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.TrimLeft(b.String(), "._-")
	if len(out) > 64 {
		out = out[:64]
	}
	return strings.TrimRight(out, "-")
}
