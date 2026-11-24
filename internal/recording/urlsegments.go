package recording

import (
	"regexp"
	"strings"
	"unicode"
)

// Path-embedded secrets (R16-D2): reset links, magic links and invite codes
// carry their token as a path segment ("/reset/<token>", "#/verify/<token>").

const minTokenSegment = 24

var uuidSegmentRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// tokenSegment reports a path segment that looks like a credential: 24 or
// more token characters mixing letters and digits. UUIDs are record ids and
// stay, because replay templates them as inputs.
func tokenSegment(seg string) bool {
	if len(seg) < minTokenSegment || uuidSegmentRe.MatchString(seg) {
		return false
	}
	var letter, digit bool
	for _, r := range seg {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r):
			digit = true
		case r == '-' || r == '_' || r == '.' || r == '~' || r == '%' || r == '=' || r == '+':
		default:
			return false
		}
	}
	return letter && digit
}

// sanitizeSrcset runs every candidate URL of a srcset through SanitizeURL
// and keeps its descriptor.
func sanitizeSrcset(v string) string {
	cands := strings.Split(v, ",")
	for i, c := range cands {
		f := strings.Fields(c)
		if len(f) == 0 {
			continue
		}
		f[0] = SanitizeURL(f[0])
		cands[i] = strings.Join(f, " ")
	}
	return strings.Join(cands, ", ")
}

// redactPathTokens replaces every token-like segment of a "/"-separated path.
func redactPathTokens(path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if tokenSegment(p) {
			parts[i] = Redacted
		}
	}
	return strings.Join(parts, "/")
}
