package recording

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// Network privacy re-check. The recorder already redacts what it sends in
// an op:"network" frame (recorder_privacy.js redactNet); ingest applies the
// same rules again, because the extension is not the only thing that can
// write to this socket and network.jsonl feeds `record api`'s generated
// workflows.

// Caps for one network line.
const (
	MaxNetBodyBytes = 8 << 10
	MaxNetHeaders   = 40
	maxHeaderValue  = 200
	maxHeaderName   = 100
)

// credentialHeaders are request headers whose value is a credential.
var credentialHeaders = map[string]bool{
	"authorization": true, "proxy-authorization": true, "cookie": true,
	"x-api-key": true, "api-key": true, "apikey": true, "x-auth-token": true,
	"x-csrf-token": true, "x-xsrf-token": true, "csrf-token": true, "x-access-token": true,
}

// credentialHeaderRe catches the custom variants (x-goog-api-key,
// x-amz-security-token, x-session-id, …).
var credentialHeaderRe = regexp.MustCompile(`(?i)token|secret|auth|api-?key|session|csrf|xsrf|signature|password|credential`)

// CredentialHeader reports whether a request header carries a credential.
func CredentialHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return credentialHeaders[n] || credentialHeaderRe.MatchString(n)
}

var (
	jwtRe         = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
	bearerRe      = regexp.MustCompile(`(?i)^(bearer|basic|token|digest)\s`)
	schemeRe      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,19}$`)
	methodRe      = regexp.MustCompile(`^[A-Z]{1,16}$`)
	cardRe        = regexp.MustCompile(`\d(?:[ -]?\d){12,18}`)
	bodySecretRe  = regexp.MustCompile(`(?i)pass|pwd|token|secret|signature|session|cookie|credential|private|auth($|[^o])|api_?key|(^|[^a-z])(pin|cvv|cvc|csc|otp|ssn|2fa|mfa|key|code)([^a-z]|$)`)
	headerValueRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)
)

// credentialValue reports a header value that is a credential whatever the
// header is called: a JWT or an auth scheme followed by a token.
func credentialValue(v string) bool {
	return jwtRe.MatchString(v) || bearerRe.MatchString(strings.TrimSpace(v))
}

// SanitizeNet makes a network line safe to store: the URL sanitised,
// cookie headers dropped, credential header values removed (the name, and
// Authorization's scheme, kept), bodies redacted and capped. A request
// body that is neither JSON nor a form, or a response sample that is not
// JSON, is dropped.
func SanitizeNet(n *NetEntry) {
	if n == nil {
		return
	}
	n.URL = SanitizeURL(n.URL)
	n.Method = strings.ToUpper(strings.TrimSpace(n.Method))
	if !methodRe.MatchString(n.Method) {
		n.Method = "GET"
	}
	if n.Initiator != "fetch" && n.Initiator != "xhr" {
		n.Initiator = ""
	}
	n.ContentType = clipRunes(strings.TrimSpace(n.ContentType), maxHeaderValue)
	n.RequestType = clipRunes(strings.TrimSpace(n.RequestType), maxHeaderValue)
	n.RequestHeaders = sanitizeHeaders(n.RequestHeaders)
	n.RequestBody = RedactBody(n.RequestBody, n.RequestType)
	n.ResponseSample = RedactBody(n.ResponseSample, "application/json")
	if !json.Valid([]byte(n.ResponseSample)) {
		n.ResponseSample = ""
	}
}

func sanitizeHeaders(in []NetHeader) []NetHeader {
	var out []NetHeader
	for _, h := range in {
		name := strings.TrimSpace(h.Name)
		lower := strings.ToLower(name)
		if name == "" || len(name) > maxHeaderName || lower == "cookie" || lower == "set-cookie" {
			continue
		}
		if len(out) >= MaxNetHeaders {
			break
		}
		clean := NetHeader{Name: name}
		value := headerValueRe.ReplaceAllString(h.Value, "")
		if h.Credential || CredentialHeader(name) || credentialValue(value) {
			clean.Credential = true
			scheme := strings.TrimSpace(h.Scheme)
			if scheme == "" && lower == "authorization" {
				scheme, _, _ = strings.Cut(strings.TrimSpace(value), " ")
			}
			if lower == "authorization" || lower == "proxy-authorization" {
				if schemeRe.MatchString(scheme) && !credentialValue(scheme) {
					clean.Scheme = scheme
				}
			}
		} else {
			switch lower {
			case "referer", "origin", "location", "content-location":
				value = SanitizeURL(value)
			}
			clean.Value = clipRunes(value, maxHeaderValue)
		}
		out = append(out, clean)
	}
	return out
}

// RedactBody returns body with every credential-looking value replaced by
// Redacted: JSON keys and form fields that name a secret, JWTs anywhere,
// card numbers. JSON stays valid JSON. Anything else, or a result over
// MaxNetBodyBytes, becomes "".
func RedactBody(body, contentType string) string {
	b := strings.TrimSpace(body)
	if b == "" {
		return ""
	}
	ct := strings.ToLower(contentType)
	var out string
	switch {
	case json.Valid([]byte(b)):
		var v any
		if err := json.Unmarshal([]byte(b), &v); err != nil {
			return ""
		}
		enc, err := json.Marshal(redactJSON(v, ""))
		if err != nil {
			return ""
		}
		out = string(enc)
	case strings.Contains(ct, "x-www-form-urlencoded"):
		out = redactForm(b)
	default:
		return ""
	}
	if len(out) > MaxNetBodyBytes {
		return ""
	}
	return out
}

func redactJSON(v any, key string) any {
	if key != "" && bodySecretRe.MatchString(key) {
		if _, isArr := v.([]any); !isArr {
			return Redacted
		}
	}
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			t[k] = redactJSON(sub, k)
		}
		return t
	case []any:
		for i, sub := range t {
			t[i] = redactJSON(sub, key)
		}
		return t
	case string:
		return redactString(t)
	}
	return v
}

func redactString(s string) string {
	if credentialValue(s) {
		return Redacted
	}
	return cardRe.ReplaceAllStringFunc(s, func(m string) string {
		if luhn(m) {
			return "[card]"
		}
		return m
	})
}

func redactForm(raw string) string {
	parts := strings.Split(raw, "&")
	for i, p := range parts {
		k, v, _ := strings.Cut(p, "=")
		key, err := url.QueryUnescape(k)
		if err != nil {
			key = k
		}
		val, err := url.QueryUnescape(v)
		if err != nil {
			val = v
		}
		if bodySecretRe.MatchString(key) || sensitiveParam(key) || credentialValue(val) {
			parts[i] = k + "=" + Redacted
			continue
		}
		if r := redactString(val); r != val {
			parts[i] = k + "=" + url.QueryEscape(r)
		}
	}
	return strings.Join(parts, "&")
}

// luhn reports a 13-19 digit card number (spaces and dashes allowed).
func luhn(s string) bool {
	var digits []int
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits = append(digits, int(r-'0'))
		case r == ' ' || r == '-':
		default:
			return false
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	for i := range digits {
		d := digits[len(digits)-1-i]
		if i%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}
