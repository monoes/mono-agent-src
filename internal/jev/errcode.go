package jev

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// errorCode is what a short machine error code looks like.
var errorCode = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

// ErrorCode is the one token of a provider's error body an adapter may put
// in an error: the first of error.code, error.type, error (a string), code
// and type that is a string matching ^[a-z0-9_.-]{1,64}$ ("" when none
// does, the body is no JSON object, or the token holds key or 8 or more
// characters of it). Nothing else
// of a response body may reach an error: it is text the provider (or
// whatever answers on its port) chose, and errors end up in stored
// rationales and agent context.
func ErrorCode(body []byte, key string) string {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return ""
	}
	var nested map[string]json.RawMessage
	_ = json.Unmarshal(top["error"], &nested)
	for _, raw := range []json.RawMessage{nested["code"], nested["type"], top["error"], top["code"], top["type"]} {
		var s string
		if json.Unmarshal(raw, &s) != nil || !errorCode.MatchString(s) {
			continue
		}
		if key != "" && (strings.Contains(s, key) || len(s) >= 8 && strings.Contains(key, s)) {
			return "" // the key, or a piece of it long enough to matter
		}
		return s
	}
	return ""
}

// HTTPError is the error text an adapter gives a non-2xx reply:
// "<adapter>: HTTP <status>", with " (<code>)" when the body carries an
// ErrorCode.
func HTTPError(adapter string, status int, body []byte, key string) string {
	if code := ErrorCode(body, key); code != "" {
		return fmt.Sprintf("%s: HTTP %d (%s)", adapter, status, code)
	}
	return fmt.Sprintf("%s: HTTP %d", adapter, status)
}
