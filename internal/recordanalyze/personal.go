package recordanalyze

import (
	"regexp"
	"strings"
)

// addressRe: a street number next to a street word, or a number-led
// comma-separated line ("221B Baker Street, London").
var addressRe = regexp.MustCompile(`(?i)\d+[a-z]?\s+\S+.*\b(st|street|rd|road|ave|avenue|blvd|boulevard|lane|ln|drive|dr|way|court|ct|square|place|pl|platz|strasse|straße|rue)\b|^\d+[a-z]?\s+[^,]+,\s*\S+`)

// personalValue reports a typed value that looks like an email address,
// phone number or street address: it goes to the model as its {{input}}
// placeholder, not as the value (R16-D3).
func personalValue(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	return emailRe.MatchString(v) || phoneRe.MatchString(v) || addressRe.MatchString(v)
}
