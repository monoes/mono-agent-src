package recording

import (
	"net/url"
	"strings"
)

// URL sanitising (security review M5). Recorded URLs end up in
// events.jsonl, the analyzer's prompt and saved actions, so credentials that
// ride in a URL — OAuth codes, magic links, signed URLs, reset tokens — are
// redacted before anything reaches disk.

// Redacted replaces a sensitive query value.
const Redacted = "REDACTED"

// sensitiveParams are query keys (lowercased) whose values are redacted.
var sensitiveParams = map[string]bool{
	"code": true, "token": true, "access_token": true, "id_token": true,
	"refresh_token": true, "state": true, "sig": true, "signature": true,
	"key": true, "api_key": true, "apikey": true, "password": true,
	"passwd": true, "auth": true, "session": true, "sessionid": true,
	"magic": true, "reset": true, "otp": true, "secret": true,
}

// sensitiveParamParts catch the variants the exact list misses
// (x-amz-signature, reset_token, client_secret, …).
var sensitiveParamParts = []string{"token", "secret", "password", "signature", "session"}

func sensitiveParam(key string) bool {
	k := strings.ToLower(key)
	if sensitiveParams[k] {
		return true
	}
	for _, part := range sensitiveParamParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

// SanitizeURL drops a URL's fragment and redacts sensitive query values.
//
// One fragment shape is kept: a hash route ("#/inbox", "#!/settings")
// with no '=' or '?' in it, because single-page apps navigate by it and a
// replay without it lands on the wrong screen. Anything that could carry a
// value (OAuth implicit-flow "#access_token=…") is dropped.
func SanitizeURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}
	u, err := url.Parse(s)
	if err != nil {
		if i := strings.IndexByte(s, '#'); i >= 0 {
			s = s[:i]
		}
		if i := strings.IndexByte(s, '?'); i >= 0 {
			s = s[:i] // cannot redact what will not parse: drop the query
		}
		return s
	}
	if u.Fragment != "" || u.RawFragment != "" {
		frag := u.Fragment
		if !(strings.HasPrefix(frag, "/") || strings.HasPrefix(frag, "!/")) || strings.ContainsAny(frag, "=?&") {
			u.Fragment, u.RawFragment = "", ""
		}
	}
	if u.RawQuery != "" {
		u.RawQuery = redactQuery(u.RawQuery)
	}
	u.User = nil
	return u.String()
}

// redactQuery rewrites a raw query, keeping order and untouched pairs
// byte-for-byte.
func redactQuery(raw string) string {
	parts := strings.Split(raw, "&")
	for i, p := range parts {
		k, _, hasValue := strings.Cut(p, "=")
		key, err := url.QueryUnescape(k)
		if err != nil {
			key = k
		}
		if hasValue && sensitiveParam(key) {
			parts[i] = k + "=" + Redacted
		}
	}
	return strings.Join(parts, "&")
}

// sanitizeEventURLs applies SanitizeURL to every URL an event carries.
func sanitizeEventURLs(ev *Event) {
	ev.URL = SanitizeURL(ev.URL)
	if ev.Target != nil && ev.Target.Href != "" {
		ev.Target.Href = SanitizeURL(ev.Target.Href)
	}
}
