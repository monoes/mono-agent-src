package guardrails

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

// Secret-key detection: known token formats always count; other
// whitespace-separated tokens count when they look random enough for the
// configured permissiveness.

// secretPatterns are token formats with a recognisable vendor shape.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----(?s:.*?)-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}\b`),
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`\b(?:sk|pk|rk)_(?:live|test)_[A-Za-z0-9]{16,}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`),
	regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`\bhf_[A-Za-z0-9]{30,}\b`),
	regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`),
	regexp.MustCompile(`\bshp(?:at|ss|ca|pa)_[a-fA-F0-9]{32}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
}

// credentialNames are the names of a credential written as name=value or
// name: value.
var credentialNames = []string{
	"api[_-]?key", "api[_-]?secret", "secret[_-]?key", "client[_-]?secret",
	"access[_-]?token", "auth[_-]?token", "pass(?:word|wd)?", "pwd", "token", "secret",
}

// assignedSecret matches such an assignment; group 1 is the value.
var assignedSecret = regexp.MustCompile(`(?i)\b(?:` + strings.Join(credentialNames, "|") +
	`)\b["']?\s*[:=]\s*["']?([^\s"',;]{6,})`)

// secretLevel is one permissiveness preset.
type secretLevel struct {
	minLen       int
	minEntropy   float64
	minDiversity int
	assignments  bool // name=value credentials count
	exemptions   bool // URLs, file names and UUIDs are never secrets
}

var secretLevels = map[string]secretLevel{
	"strict":     {minLen: 10, minEntropy: 3.0, minDiversity: 2, assignments: true},
	"balanced":   {minLen: 16, minEntropy: 3.5, minDiversity: 3, assignments: true, exemptions: true},
	"permissive": {minLen: 32, minEntropy: 4.0, minDiversity: 3, exemptions: true},
}

var (
	uuidRe     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	plainURLRe = regexp.MustCompile(`^(?i:https?)://[A-Za-z0-9.-]+(?::\d+)?(?:/[A-Za-z0-9._/-]*)?$`)
	fileNameRe = regexp.MustCompile(`^[^\s]*\.(?i:py|js|ts|jsx|tsx|go|rs|rb|php|java|c|h|cpp|cs|sh|bat|html|css|json|md|txt|csv|xml|ya?ml|ini|conf|config|log|sql|pdf|docx?|xlsx?|pptx?|jpe?g|png|gif|svg|zip|tar|gz)$`)
)

// detectSecrets returns the secret values found in text.
func detectSecrets(text, permissiveness string) []string {
	lvl, ok := secretLevels[permissiveness]
	if !ok {
		lvl = secretLevels["balanced"]
	}
	var claimed spanSet
	var found []string
	claim := func(a, b int) {
		if !claimed.overlaps(a, b) {
			claimed.add(a, b)
			found = append(found, text[a:b])
		}
	}
	for _, re := range secretPatterns {
		for _, m := range re.FindAllStringIndex(text, -1) {
			claim(m[0], m[1])
		}
	}
	if lvl.assignments {
		for _, m := range assignedSecret.FindAllStringSubmatchIndex(text, -1) {
			claim(m[2], m[3])
		}
	}
	for _, f := range tokenSpans(text) {
		if !claimed.overlaps(f[0], f[1]) && randomLooking(text[f[0]:f[1]], lvl) {
			claim(f[0], f[1])
		}
	}
	return found
}

// tokenSpans splits text on whitespace and trims wrapping punctuation
// (quotes, brackets, markdown emphasis, a sentence's final period).
func tokenSpans(text string) [][2]int {
	var out [][2]int
	const wrap = "\"'`()[]{}<>,;*#"
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		a, b := start, end
		for a < b && strings.IndexByte(wrap, text[a]) >= 0 {
			a++
		}
		for a < b && strings.IndexByte(wrap+".:!?", text[b-1]) >= 0 {
			b--
		}
		if a < b {
			out = append(out, [2]int{a, b})
		}
		start = -1
	}
	for i, r := range text {
		if unicode.IsSpace(r) {
			flush(i)
		} else if start < 0 {
			start = i
		}
	}
	flush(len(text))
	return out
}

func randomLooking(tok string, lvl secretLevel) bool {
	if len(tok) < lvl.minLen || diversity(tok) < lvl.minDiversity {
		return false
	}
	if lvl.exemptions && (uuidRe.MatchString(tok) || plainURLRe.MatchString(tok) || fileNameRe.MatchString(tok)) {
		return false
	}
	return entropy(tok) >= lvl.minEntropy
}

// entropy is the Shannon entropy of s in bits per character.
func entropy(s string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	var h float64
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// diversity counts the character classes in s: lower, upper, digit, other.
func diversity(s string) int {
	var lower, upper, digit, other bool
	for _, r := range s {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	n := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			n++
		}
	}
	return n
}
