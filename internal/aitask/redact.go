package aitask

import (
	"fmt"
	"github.com/monoes/mono-agent/internal/workflow"
	"regexp"
	"strings"
	"unicode"
)

// RedactedValue replaces what looks like a secret in a result.
const RedactedValue = "[redacted]"

var (
	// placeholderShape is a value that names a secret instead of holding one: a
	// double-brace secret or vault reference ({{secret:NAME}}), an @secret:NAME
	// reference (the node-config form), a double-brace expression ({{ $json.x }})
	// or the REPLACE_ME a draft leaves. The WHOLE value must match, and a NAME
	// must be a plain identifier that does not itself look like a secret
	// (isPlaceholder): a real secret placed where the name goes is not a
	// placeholder.
	placeholderShape = regexp.MustCompile(`^(?:\{\{\s*(?:secret|vault)\s*[:.]\s*([A-Za-z][A-Za-z0-9_-]{0,63})\s*\}\}|@secret:([A-Za-z][A-Za-z0-9_-]{0,63})|\{\{\s*\$[A-Za-z0-9_.: -]{0,100}\}\}|(REPLACE_ME))$`)
	digitRun         = regexp.MustCompile(`[0-9]{6,}`)

	// The shapes of a secret anywhere inside a string. RE2: linear in the
	// text, no backtracking, so a hostile result cannot make this slow.
	embeddedToken = regexp.MustCompile(`\b(?:(?:sk|rk|pk)[-_](?:live|test)[-_][A-Za-z0-9_-]{8,}|glpat-[A-Za-z0-9_-]{16,}|npm_[A-Za-z0-9]{30,}|sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{16,}|xox[a-z]-[A-Za-z0-9-]{8,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{30,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]*)?)`)
	privateKey    = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY`)
	bearerToken   = regexp.MustCompile(`(?i)\bBearer\s+(\S{16,})`)
	basicAuth     = regexp.MustCompile(`(?i)(\bAuthorization\s*[:=]\s*["']?\s*)Basic\s+(\S{8,})`)
	basicValue    = regexp.MustCompile(`(?i)^\s*Basic\s+\S{8,}`)
	urlUserinfo   = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://[^\s:/@]+:)([^\s@/]+)(@)`)
	queryParam    = regexp.MustCompile(`(?i)([?&](?:(?:access|refresh|id)_` + `tok` + `en=|` + `tok` + `en=)|[?&](?:api[_-]?key|client_secret|secret|password|passwd)=)([^&\s"'#]{8,})`)
	// A secret-named label with its value in prose ("password: Hunter2!"). The
	// value must hold something other than lowercase letters, so a sentence
	// such as "password: required" stays.
	labelledValue = regexp.MustCompile(`(?i)\b(pass(?:word|wd|code|phrase)|pwd|secret|api[ _-]?key|` + `tok` + `en)(["']?\s*[:=]\s*["']?)(\S{8,})`)
)

// secretWords and secretPairs name a secret when they are WORDS of a label or
// key (split on separators and camelCase), so "footprint" is not "otp" and
// "tokenizer" is not a token.
var (
	secretWords = map[string]bool{"password": true, "passwd": true, "pass": true, "passcode": true, "passphrase": true, "passport": true,
		"pwd": true, "pw": true, "secret": true, "tok" + "en": true, "apikey": true, "otp": true, "2fa": true, "cvv": true, "cvc": true, "ssn": true, "iban": true, "privatekey": true,
		"totp": true, "mfa": true, "authenticator": true}
	secretPairs = map[string]bool{"api key": true, "private key": true, "card number": true, "card no": true, "social security": true,
		"access key": true, "secret key": true,
		// one-time codes, as a form labels them (a fill reports labels and values)
		"one time": true, "security code": true, "verification code": true, "sms code": true, "login code": true, "authentication code": true,
		"confirmation code": true, "digit code": true, "backup code": true, "backup codes": true, "recovery code": true, "recovery codes": true}
	// A key that also holds one of these words describes a secret (its name, its
	// limit, its type) and is not one.
	metadataWords = map[string]bool{"name": true, "id": true, "ref": true, "enabled": true, "type": true, "limit": true, "count": true, "max": true,
		"min": true, "usage": true, "ttl": true, "expires": true, "expiry": true, "length": true, "len": true, "size": true, "budget": true, "label": true,
		"status": true, "field": true, "hint": true, "used": true, "total": true}
)

// labelWords splits a label or key into lowercase words: separators break it,
// and so does a lower-to-upper change (apiKey, passportNumber).
func labelWords(s string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	var prev rune
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if unicode.IsUpper(r) && unicode.IsLower(prev) {
				flush()
			}
			cur = append(cur, r)
		} else {
			flush()
		}
		prev = r
	}
	flush()
	return words
}

// isSecretLabel says whether a label or JSON key names a secret.
func isSecretLabel(s string) bool {
	words := labelWords(s)
	hit := false
	for i, w := range words {
		if secretWords[w] || (i+1 < len(words) && secretPairs[w+" "+words[i+1]]) {
			hit = true
		}
	}
	if !hit {
		return false
	}
	for _, w := range words {
		if metadataWords[w] {
			return false
		}
	}
	return true
}

// Form-field labels are the page's words, not ours: a page can call a field
// "One-time code (name)" to hit the metadata veto of a JSON key, or label a
// street "Token Street". So a label is judged by isSecretFieldLabel: no veto,
// and the words that are also ordinary ones (pass, token, pin, backup) name a
// secret only as the label itself ("PIN", "Your token"), not inside other words
// ("Boarding pass", "Token Street"). The value's own shape still decides
// separately (looksSecret).
var (
	ambiguousSecretWords = map[string]bool{"pass": true, "pw": true, "tok" + "en": true, "pin": true, "backup": true}
	// The label, once these filler words are dropped, that names a secret alone.
	exactSecretLabels = map[string]bool{"pass": true, "pw": true, "tok" + "en": true, "pin": true, "pin code": true, "pin number": true,
		"access tok" + "en": true, "api tok" + "en": true, "auth tok" + "en": true, "security tok" + "en": true, "refresh tok" + "en": true,
		"session tok" + "en": true, "backup": true}
	labelFiller = map[string]bool{"your": true, "enter": true, "the": true, "a": true, "an": true, "new": true, "current": true, "old": true,
		"my": true, "please": true, "repeat": true, "confirm": true}
	// "code" next to one of these (anywhere in the label) is a one-time code:
	// "Code from SMS", "Authenticator code", "6-digit code".
	codeQualifiers = map[string]bool{"sms": true, "authenticator": true, "verification": true, "security": true, "confirmation": true,
		"login": true, "authentication": true, "backup": true, "recovery": true, "otp": true, "digit": true, "text": true, "onetime": true, "time": true}
)

// isSecretFieldLabel says whether the label of a form field names a secret.
func isSecretFieldLabel(s string) bool {
	words := labelWords(s)
	var core []string
	for _, w := range words {
		if !labelFiller[w] {
			core = append(core, w)
		}
	}
	if exactSecretLabels[strings.Join(core, " ")] {
		return true
	}
	hasCode, qualified := false, false
	for i, w := range words {
		if w == "code" || w == "codes" {
			hasCode = true
		}
		if codeQualifiers[w] {
			qualified = true
		}
		if ambiguousSecretWords[w] {
			continue
		}
		if secretWords[w] || (i+1 < len(words) && secretPairs[w+" "+words[i+1]]) {
			return true
		}
	}
	return hasCode && qualified
}

// isPlaceholder says whether s is a whole placeholder whose name is only a name.
func isPlaceholder(s string) bool {
	m := placeholderShape.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	name := m[1] + m[2]
	if name == "" {
		return true // an expression or REPLACE_ME
	}
	if embeddedToken.MatchString(name) || digitRun.MatchString(name) {
		return false
	}
	// A random mixed-case alphanumeric name ("Hunter2Hunter2xx") is a secret in
	// disguise; a name is lower case, or SHOUTING, or has no digits.
	hasUpper, hasLower, hasDigit := false, false, false
	for _, r := range name {
		hasUpper = hasUpper || (r >= 'A' && r <= 'Z')
		hasLower = hasLower || (r >= 'a' && r <= 'z')
		hasDigit = hasDigit || (r >= '0' && r <= '9')
	}
	return !(hasUpper && hasLower && hasDigit)
}

func looksSecret(label, value string) bool {
	if isPlaceholder(value) {
		return false
	}
	return isSecretFieldLabel(label) || redactString(strings.TrimSpace(value)) != strings.TrimSpace(value)
}

// hasMoreThanLowercase is true when s holds a digit, an upper-case letter or a symbol.
func hasMoreThanLowercase(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z') {
			return true
		}
	}
	return false
}

// zeroWidth are the characters that split a token without showing: zero-width
// spaces and joiners, direction marks, the word joiner and the byte-order mark.
func isZeroWidth(r rune) bool {
	return (r >= 0x200B && r <= 0x200F) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2060 && r <= 0x2064) || (r >= 0x2066 && r <= 0x2069) || r == 0xFEFF
}

func stripZeroWidth(s string) string {
	if !strings.ContainsFunc(s, isZeroWidth) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isZeroWidth(r) {
			return -1
		}
		return r
	}, s)
}

// headerValue* find a credential header and its value on the same line.
var (
	cookieHeader = regexp.MustCompile(`(?i)\b((?:set-)?cookie\s*:\s*)([^\r\n]*=[^\r\n]{3,})`)
	keyHeader    = regexp.MustCompile(`(?i)\b((?:x-api-key|x-auth-` + `tok` + `en)\s*:\s*)(\S{8,})`)
	schemeHeader = regexp.MustCompile(`(?i)\b((?:proxy-)?authorization\s*:\s*["']?\s*(?:Token|ApiKey|Digest)\s+)(\S{8,})`)
)

// RedactText is the value scanner for free text (run output, error messages):
// the secrets inside s are replaced, the rest is kept.
func RedactText(s string) string { return redactString(s) }

// redactString removes what looks like a secret from s: every key- or
// token-shaped word inside it, a private key block, a bearer, basic or token
// credential, a cookie or key header, the password of a URL, the value of a
// credential query parameter and the value of a secret-named label in prose.
// A placeholder stays; it is accepted only as the whole string (or the whole
// value). Zero-width characters are removed first so they cannot split a
// token. Every step is one linear pass.
func redactString(s string) string {
	s = stripZeroWidth(s)
	if isPlaceholder(s) {
		return s
	}
	if privateKey.MatchString(s) {
		return RedactedValue
	}
	// Each pattern runs only when its trigger text is there at all: ordinary
	// text pays for a few substring scans, not for a dozen regex passes.
	low := strings.ToLower(s)
	has := func(subs ...string) bool {
		for _, sub := range subs {
			if strings.Contains(low, sub) {
				return true
			}
		}
		return false
	}
	if has("sk", "rk", "pk", "gh", "xox", "akia", "aiza", "eyj", "glpat", "npm_") {
		s = embeddedToken.ReplaceAllString(s, RedactedValue)
	}
	keepIfPlaceholder := func(re *regexp.Regexp, valueGroup int) {
		s = replaceGroup(s, re, valueGroup, func(v string) bool { return isPlaceholder(strings.Trim(v, `"',;`)) })
	}
	if has("bearer") {
		s = bearerToken.ReplaceAllStringFunc(s, func(m string) string {
			f := strings.Fields(m)
			if isPlaceholder(f[len(f)-1]) {
				return m
			}
			return f[0] + " " + RedactedValue
		})
	}
	if has("authorization") {
		s = basicAuth.ReplaceAllString(s, "${1}Basic "+RedactedValue)
		keepIfPlaceholder(schemeHeader, 2)
	}
	if has("://") {
		s = urlUserinfo.ReplaceAllStringFunc(s, func(m string) string {
			g := urlUserinfo.FindStringSubmatch(m)
			if isPlaceholder(g[2]) {
				return m
			}
			return g[1] + RedactedValue + g[3]
		})
	}
	if has("?", "&") {
		keepIfPlaceholder(queryParam, 2)
	}
	if has("x-api-key", "x-auth-") {
		keepIfPlaceholder(keyHeader, 2)
	}
	if has("cookie") {
		s = cookieHeader.ReplaceAllString(s, "${1}"+RedactedValue)
	}
	return redactLabelled(s)
}

// replaceGroup replaces group g of every match of re with the redaction marker,
// except where keep says the value is to stay. One pass.
func replaceGroup(s string, re *regexp.Regexp, g int, keep func(string) bool) string {
	var out strings.Builder
	last := 0
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		if loc[2*g] < 0 || keep(s[loc[2*g]:loc[2*g+1]]) {
			continue
		}
		out.WriteString(s[last:loc[2*g]])
		out.WriteString(RedactedValue)
		last = loc[2*g+1]
	}
	if last == 0 {
		return s
	}
	out.WriteString(s[last:])
	return out.String()
}

// redactLabelled redacts the value of a secret-named label in prose
// ("password: Hunter2!", "\"api_key\": \"abc12345\""). Whether a label sits
// inside an open double-brace reference is tracked as the text is scanned once,
// so the work is linear in the text however many labels it holds.
func redactLabelled(s string) string {
	matches := labelledValue.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s
	}
	var out strings.Builder
	last, scanned, open := 0, 0, false
	for _, loc := range matches {
		// Advance the brace state over s[scanned:loc[0]]; a pair cut by loc[0] is looked at next time.
		for scanned+1 < loc[0] {
			switch {
			case s[scanned] == '{' && s[scanned+1] == '{':
				open, scanned = true, scanned+2
			case s[scanned] == '}' && s[scanned+1] == '}':
				open, scanned = false, scanned+2
			default:
				scanned++
			}
		}
		if loc[0] > 0 && s[loc[0]-1] == '@' { // @secret:NAME is a reference
			continue
		}
		if open { // "secret:" inside an open reference is its prefix, not a label
			continue
		}
		raw := s[loc[6]:loc[7]]
		v := strings.Trim(raw, `"',;)}]`)
		if v == "" || isPlaceholder(v) || !hasMoreThanLowercase(v) || strings.HasPrefix(raw, RedactedValue) {
			continue
		}
		lead := strings.Index(raw, v)
		start, end := loc[6]+lead, loc[6]+lead+len(v)
		if start < last {
			continue
		}
		out.WriteString(s[last:start])
		out.WriteString(RedactedValue)
		last = end
	}
	if last == 0 {
		return s
	}
	out.WriteString(s[last:])
	return out.String()
}

func has(m map[string]any, k string) bool { _, ok := m[k]; return ok }

// labelKeys name the key of a {label, value} pair in a field report.
var labelKeys = []string{"label", "name", "field"}

// RedactResult returns v (decoded JSON) with secrets removed, before the
// result is stored: the string value under a key that names a secret, the
// value of a {label, value} pair whose label names one (or whose value looks
// like one), a secret-shaped key name, and every secret inside a string.
// Nothing in the result is trusted to have left them out.
//
// It runs after the schema check, so it only ever swaps a STRING for another
// string: numbers, booleans, nulls, lists and objects keep their type (a count
// under a key such as a token limit stays a number), and the replacement is
// short and never empty, so type, length and enum checks hold. Placeholders and
// metadata keys (max_tokens, token_type, secret_name) are left alone.
func RedactResult(v any) any {
	switch t := v.(type) {
	case map[string]any:
		label := ""
		for _, k := range labelKeys {
			if s, ok := t[k].(string); ok {
				label = s
				break
			}
		}
		out := make(map[string]any, len(t))
		renamed := map[string]int{} // per redacted key name: the last suffix handed out
		for k, e := range t {
			name := k
			if r := redactString(k); r != k {
				name = r
				if _, inInput := t[name]; inInput || has(out, name) {
					n := renamed[r]
					for {
						n++
						cand := fmt.Sprintf("%s %d", r, n)
						if _, inInput := t[cand]; !inInput && !has(out, cand) {
							name = cand
							break
						}
					}
					renamed[r] = n
				}
			}
			switch s := e.(type) {
			case string:
				switch {
				case isSecretLabel(k) && s != "" && !isPlaceholder(s):
					out[name] = RedactedValue
				case k == "value" && label != "" && looksSecret(label, s):
					out[name] = RedactedValue
				case strings.EqualFold(k, "authorization") && basicValue.MatchString(s):
					out[name] = "Basic " + RedactedValue
				default:
					out[name] = redactString(s)
				}
			case float64:
				// A number typed as a number under a secret LABEL ({"label":"Card number",
				// "value":4111...}) is still the secret; a count under a key stays a number.
				if k == "value" && label != "" && isSecretFieldLabel(label) {
					out[name] = RedactedValue
				} else {
					out[name] = e
				}
			default:
				out[name] = RedactResult(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = RedactResult(e)
		}
		return out
	case string:
		return redactString(t)
	}
	return v
}

func init() {
	// Free text a marked process is shown (workflow run output and errors) goes
	// through the same scanner as a task's result.
	workflow.AgentTextScrubber = RedactText
}
