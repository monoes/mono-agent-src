package recording

import (
	"strings"
	"testing"
)

// R16-05: card-like and camelCase secret field names are sensitive.
func TestSensitiveNamesCardAndCamelCase(t *testing.T) {
	for _, fp := range []*Fingerprint{
		{Name: "cardNumber"}, {Label: "Card number"}, {Name: "iban"}, {Label: "Routing number"},
		{Name: "apiKey"}, {Label: "API key"}, {Name: "credential"}, {Name: "bearer"}, {Name: "cookie"},
	} {
		if !SensitiveTarget(fp) {
			t.Errorf("%+v not sensitive", fp)
		}
	}
}

func TestRedactBodyCamelCaseKeys(t *testing.T) {
	out := RedactBody(`{"accessKey":"k-123","cardNumber":"4111111111111111","iban":"DE89370400440532013000","ok":"fine"}`, "application/json")
	for _, leak := range []string{"k-123", "4111111111111111", "DE8937"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q: %s", leak, out)
		}
	}
	if !strings.Contains(out, "fine") {
		t.Errorf("ordinary value lost: %s", out)
	}
}

// R16-06: more URL-bearing attributes, comments and style are scrubbed.
func TestScrubHTMLMoreURLAttributesAndComments(t *testing.T) {
	src := `<!-- sekrit-comment --><video poster="https://x.test/p.png?token=sekrit-poster"></video>` +
		`<img srcset="https://x.test/a.png?token=sekrit-set 1x, https://x.test/b.png 2x">` +
		`<object data="https://x.test/o?token=sekrit-obj"></object><a ping="https://x.test/ping?token=sekrit-ping" style="background:url(https://x.test/i?token=sekrit-style)">x</a>` +
		`<style>.a{background:url(https://x.test/i?token=sekrit-css)}</style><p>ok</p>`
	got := ScrubHTML(src, false)
	if strings.Contains(got, "sekrit") {
		t.Errorf("leak in %s", got)
	}
	if !strings.Contains(got, "<p>ok</p>") || !strings.Contains(got, "https://x.test/b.png 2x") {
		t.Errorf("content lost in %s", got)
	}
}

// R16-D2: long mixed letter+digit path segments and hash-route segments are redacted.
func TestSanitizeURLRedactsTokenPathSegments(t *testing.T) {
	tok := "a1b2c3d4e5f6a7b8c9d0e1f2g3"
	got := SanitizeURL("https://app.test/reset/" + tok + "/confirm")
	if strings.Contains(got, tok) || !strings.Contains(got, "/reset/") || !strings.Contains(got, "/confirm") {
		t.Errorf("path token: %s", got)
	}
	got = SanitizeURL("https://app.test/#/verify/" + tok)
	if strings.Contains(got, tok) || !strings.Contains(got, "#/verify/") {
		t.Errorf("hash route token: %s", got)
	}
	for _, keep := range []string{"https://app.test/a-very-long-readable-article-name-here", "https://app.test/users/12345678901234567890123456"} {
		if got := SanitizeURL(keep); got != keep {
			t.Errorf("%s changed to %s", keep, got)
		}
	}
}
