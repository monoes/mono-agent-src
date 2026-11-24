// Package chromecookies holds the on-disk shape of a Chrome session cookie
// as stored in crawler_sessions.cookies_json. The login flow converts raw
// chrome.cookies.getAll() results from the extension bridge into this shape.
package chromecookies

import (
	"fmt"
	"strings"
)

// Cookie mirrors proto.NetworkCookieParam's JSON shape (name, value, domain,
// path, secure, httpOnly, sameSite, expires), which is what
// crawler_sessions.cookies_json already stores and what run.go's
// session-restore unmarshals into, so no downstream code needs to change to
// consume cookies read this way. HostOnly is not part of that shape (that
// unmarshal ignores it): it records that the cookie was set for exactly
// Domain and no subdomain, so a restore can keep it so.
type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Secure   bool    `json:"secure,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	SameSite string  `json:"sameSite,omitempty"` // "Strict", "Lax" or "None"; empty when unspecified
	HostOnly bool    `json:"hostOnly,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
}

// FromExtension converts the raw chrome.cookies.getAll() result returned by the extension
// bridge (name, value, domain, path, secure, httpOnly, hostOnly, sameSite, expirationDate)
// into Cookies. When domains is not empty only the cookies that belong to one of them are
// kept (see InDomains): a login tab can hold cookies of other sites, which are not the
// platform's session.
func FromExtension(raw interface{}, domains []string) ([]Cookie, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected cookies payload type %T", raw)
	}
	cookies := make([]Cookie, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		c := Cookie{
			Name:     str(m, "name"),
			Value:    str(m, "value"),
			Domain:   str(m, "domain"),
			Path:     str(m, "path"),
			Secure:   flag(m, "secure"),
			HTTPOnly: flag(m, "httpOnly"),
			HostOnly: flag(m, "hostOnly"),
			SameSite: sameSite(str(m, "sameSite")),
		}
		if exp, ok := m["expirationDate"].(float64); ok {
			c.Expires = exp
		}
		if len(domains) > 0 && !InDomains(c.Domain, domains) {
			continue
		}
		cookies = append(cookies, c)
	}
	return cookies, nil
}

// InDomains reports whether a cookie domain (with or without the leading dot) is one of
// domains or a subdomain of one. Domains may be given with a leading dot or a "*." prefix.
func InDomains(cookieDomain string, domains []string) bool {
	cd := strings.ToLower(strings.TrimPrefix(cookieDomain, "."))
	if cd == "" {
		return false
	}
	for _, d := range domains {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(d, "*"), "."))
		if d != "" && (cd == d || strings.HasSuffix(cd, "."+d)) {
			return true
		}
	}
	return false
}

// sameSite maps chrome.cookies' sameSite ("no_restriction", "lax", "strict", "unspecified")
// to the DevTools names.
func sameSite(v string) string {
	switch strings.ToLower(v) {
	case "no_restriction", "none":
		return "None"
	case "lax":
		return "Lax"
	case "strict":
		return "Strict"
	}
	return ""
}

func str(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func flag(m map[string]interface{}, key string) bool {
	v, _ := m[key].(bool)
	return v
}
