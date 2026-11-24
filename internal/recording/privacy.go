package recording

import (
	"bytes"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// Privacy re-check (spec §8.2, security review H6/L1). The recorder already
// withholds these values; ingest checks again because the extension is not
// the only thing that can write to this socket, and a value that reached
// disk is a value that reaches the analyzer's prompt.

// sensitiveNameRe matches field names and labels that announce a secret.
// Deliberately broad: masking a harmless field costs a manual input at
// review time, missing a secret costs the secret.
var sensitiveNameRe = regexp.MustCompile(`(?i)pass|pwd|token|secret|card|iban|routing|credential|api.?key|bearer|cookie|(^|[^a-z])(pin|cvv|cvc|csc|otp|ssn|2fa|mfa)([^a-z]|$)`)

// SensitiveTarget reports whether a fingerprint names a field whose value
// must never be stored: the recorder flagged it, it is a password or hidden
// input, the page marks it with a card / password autocomplete token, or
// its name or label says it holds a secret.
func SensitiveTarget(fp *Fingerprint) bool {
	if fp == nil {
		return false
	}
	if fp.Sensitive {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(fp.InputType)) {
	case "password", "hidden":
		return true
	}
	if sensitiveAutocomplete(fp.Autocomplete) {
		return true
	}
	for _, s := range []string{fp.Name, fp.ID, fp.Label, fp.AriaName, fp.Placeholder, fp.TestID} {
		if s != "" && sensitiveNameRe.MatchString(s) {
			return true
		}
	}
	return false
}

func sensitiveAutocomplete(ac string) bool {
	for _, tok := range strings.Fields(strings.ToLower(ac)) {
		if strings.HasPrefix(tok, "cc-") || tok == "current-password" || tok == "new-password" || tok == "one-time-code" {
			return true
		}
	}
	return false
}

// Length caps for free text an event or start frame carries.
const (
	MaxGoalRunes  = 500
	MaxTitleRunes = 300
	MaxNoteRunes  = 500
)

// Sanitize makes an event safe to store: URLs sanitised, the value of a
// sensitive field dropped and marked Masked, free text capped.
func Sanitize(ev *Event) {
	if ev == nil {
		return
	}
	sanitizeEventURLs(ev)
	ev.Note = clipRunes(ev.Note, MaxNoteRunes)
	if !SensitiveTarget(ev.Target) {
		return
	}
	ev.Target.Sensitive = true
	if ev.Value != "" || ev.Type == EvType || ev.Type == EvSelect {
		ev.Value = ""
		ev.Masked = true
	}
}

// CleanGoal is a recording's goal as it is kept and handed to the analysis
// (cleanLine, at most MaxGoalRunes runes).
func CleanGoal(s string) string { return cleanLine(s, MaxGoalRunes) }

// cleanTitle is a page's title as a recording keeps it: the page wrote it.
func cleanTitle(s string) string { return cleanLine(s, MaxTitleRunes) }

// cleanLine is model- or page-written text as a recording keeps it: one line
// of at most max runes, without what shows nothing on screen but a model
// still reads (invisible). What text needs stays, and no more, so what is
// kept carries no hidden bits: a ZWJ that joins emoji, or a conjunct after a
// virama or coeng; a ZWNJ inside an Arabic-script or Indic word; VS16 that
// makes an emoji, and VS15 or VS16 on a keycap; a direction mark where the
// text turns between right-to-left and left-to-right letters, one per turn.
// A soft hyphen, and a joiner or selector that is not needed, are deleted.
func cleanLine(s string, max int) string {
	rs := []rune(strings.ToValidUTF8(s, ""))
	out := make([]rune, 0, len(rs))
	last := func() rune {
		if len(out) == 0 {
			return ' '
		}
		return out[len(out)-1]
	}
	// lastBase is the last kept character but a variation selector: what a
	// ZWJ after a heart and VS16 joins.
	lastBase := func() rune {
		for i := len(out) - 1; i >= 0; i-- {
			if out[i] != 0xFE0F && out[i] != 0xFE0E {
				return out[i]
			}
		}
		return ' '
	}
	// The direction of the last letter kept (the start counts as left-to-right),
	// and whether a direction mark was kept since it.
	lastRTL, markSinceLetter := false, false
	for i, r := range rs {
		next := ' '
		if i+1 < len(rs) {
			next = rs[i+1]
		}
		keep := true
		switch {
		case r == 0x00AD: // a soft hyphen only says where a word may break
			keep = false
		case r == 0x200D:
			keep = (emoji(lastBase()) && emoji(next)) || (virama(last()) && unicode.IsLetter(next))
		case r == 0x200C:
			keep = joinScript(last()) && joinScript(next) && unicode.IsLetter(next)
		case directionMark(r):
			keep = !markSinceLetter && lastRTL != nextLetterRTL(rs, i)
			markSinceLetter = markSinceLetter || keep
		case r == 0xFE0F:
			keep = emoji(last()) || keycap(last(), next)
		case r == 0xFE0E:
			keep = keycap(last(), next)
		case unicode.Is(unicode.Variation_Selector, r):
			keep = false
		case invisible(r):
			out = append(out, ' ')
			continue
		}
		if keep {
			out = append(out, r)
			if unicode.IsLetter(r) {
				lastRTL, markSinceLetter = rtl(r), false
			}
		}
	}
	return clipRunes(strings.Join(strings.Fields(string(out)), " "), max)
}

// rtl is a right-to-left letter: Hebrew, Arabic, Syriac, Thaana, N'Ko and the
// rest of the right-to-left blocks.
func rtl(r rune) bool {
	return unicode.IsLetter(r) && ((r >= 0x0590 && r <= 0x08FF) || (r >= 0xFB1D && r <= 0xFDFF) ||
		(r >= 0xFE70 && r <= 0xFEFF) || (r >= 0x10800 && r <= 0x10FFF) || (r >= 0x1E800 && r <= 0x1EFFF))
}

// nextLetterRTL is whether the first letter after rs[i] is right-to-left; the
// text's end counts as left-to-right.
func nextLetterRTL(rs []rune, i int) bool {
	for _, r := range rs[i+1:] {
		if unicode.IsLetter(r) {
			return rtl(r)
		}
	}
	return false
}

// emoji is a character that can be an emoji: the symbol and pictograph
// blocks, and the few older characters an emoji presentation applies to.
func emoji(r rune) bool {
	switch r {
	case 0x00A9, 0x00AE, 0x203C, 0x2049, 0x2122, 0x2139, 0x24C2, 0x3030, 0x303D, 0x3297, 0x3299:
		return true
	}
	return (r >= 0x2190 && r <= 0x21FF) || (r >= 0x2300 && r <= 0x23FF) || (r >= 0x25A0 && r <= 0x27BF) ||
		(r >= 0x2900 && r <= 0x297F) || (r >= 0x2B00 && r <= 0x2BFF) || (r >= 0x1F000 && r <= 0x1FAFF)
}

// keycap is a keycap's base (0-9, # or *) followed by the enclosing keycap.
func keycap(base, next rune) bool {
	return next == 0x20E3 && ((base >= '0' && base <= '9') || base == '#' || base == '*')
}

// virama is a virama or coeng: the mark a conjunct's ZWJ follows.
func virama(r rune) bool {
	switch r {
	case 0x094D, 0x09CD, 0x0A4D, 0x0ACD, 0x0B4D, 0x0BCD, 0x0C4D, 0x0CCD, 0x0D4D, 0x0DCA, 0x0E3A,
		0x1039, 0x103A, 0x17D2, 0x1A60, 0x1B44, 0xA8C4, 0xA9C0, 0x11046:
		return true
	}
	return false
}

// joinScript is a character of the Arabic script or the Indic blocks, the
// words a ZWNJ may sit inside.
func joinScript(r rune) bool {
	return (r >= 0x0600 && r <= 0x06FF) || (r >= 0x0750 && r <= 0x077F) || (r >= 0x08A0 && r <= 0x08FF) ||
		(r >= 0xFB50 && r <= 0xFDFF) || (r >= 0xFE70 && r <= 0xFEFF) || (r >= 0x0900 && r <= 0x0DFF)
}

// directionMark is LRM, RLM or ALM: what mixed scripts need to read right
// where the direction turns.
func directionMark(r rune) bool { return r == 0x200E || r == 0x200F || r == 0x061C }

// invisible reports a character that shows nothing: a control, the Unicode
// tag block (invisible "ASCII smuggling"), a variation selector, a format
// character (bidi controls, zero-width ones, the byte order mark), a
// private-use character, or a code point with no character assigned.
func invisible(r rune) bool {
	switch {
	case r >= 0xE0000 && r <= 0xE007F:
		return true
	case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), unicode.Is(unicode.Variation_Selector, r):
		return true
	}
	return !unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z)
}

// clipRunes caps s at n runes.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ScrubHTML rewrites a DOM snippet so it carries no secrets:
//
//   - value attributes of sensitive inputs are removed (every input's, and
//     every textarea's text, when allValues is set — the snippet belongs to
//     an event on a sensitive field);
//   - data-* attributes are removed (apps park tokens and ids in them);
//   - inline event handlers (on*) are removed: they are page code, and can
//     carry a token (onclick="fetch(…, {Authorization: 'Bearer …'})");
//   - URLs in href/src/action/formaction are sanitised (SanitizeURL), and
//     javascript:/vbscript: ones removed;
//   - <script> elements are dropped.
//
// It uses an HTML tokenizer, not a pattern: attribute order, quoting and
// case cannot hide a value from it.
func ScrubHTML(src string, allValues bool) string {
	z := html.NewTokenizer(strings.NewReader(src))
	var out bytes.Buffer
	skip := "" // element whose content is being dropped
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			// io.EOF or a malformed tail: either way, what was
			// rewritten so far is all that is kept.
			return out.String()
		}
		tok := z.Token()
		if skip != "" {
			if tt == html.EndTagToken && tok.Data == skip {
				skip = ""
				if tok.Data != "script" && tok.Data != "style" {
					out.WriteString(tok.String())
				}
			}
			continue
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			if tok.Data == "script" || tok.Data == "style" {
				if tt == html.StartTagToken {
					skip = tok.Data
				}
				continue
			}
			sensitive := allValues || sensitiveElement(tok)
			tok.Attr = scrubAttrs(tok, sensitive)
			out.WriteString(tok.String())
			if tok.Data == "textarea" && sensitive && tt == html.StartTagToken {
				skip = "textarea"
			}
		case html.CommentToken:
			// Comments can carry anything a page author or template left in.
		default:
			out.WriteString(tok.String())
		}
	}
}

// sensitiveElement reports whether a form control's value must go.
func sensitiveElement(tok html.Token) bool {
	if tok.Data != "input" && tok.Data != "textarea" {
		return false
	}
	for _, a := range tok.Attr {
		v := strings.ToLower(a.Val)
		switch strings.ToLower(a.Key) {
		case "type":
			if t := strings.TrimSpace(v); t == "password" || t == "hidden" {
				return true
			}
		case "autocomplete":
			if sensitiveAutocomplete(v) {
				return true
			}
		case "style":
			if strings.Contains(v, "-webkit-text-security") {
				return true
			}
		case "name", "id", "aria-label", "placeholder":
			if sensitiveNameRe.MatchString(a.Val) {
				return true
			}
		}
	}
	return false
}

// scriptURL reports a javascript:/vbscript: URL. Browsers skip tabs,
// newlines and other controls inside a scheme, so they are skipped here too.
func scriptURL(v string) bool {
	var b strings.Builder
	for _, r := range v {
		if r > ' ' {
			b.WriteRune(r)
		}
	}
	s := strings.ToLower(b.String())
	return strings.HasPrefix(s, "javascript:") || strings.HasPrefix(s, "vbscript:")
}

func scrubAttrs(tok html.Token, sensitive bool) []html.Attribute {
	kept := tok.Attr[:0]
	for _, a := range tok.Attr {
		key := strings.ToLower(a.Key)
		if a.Namespace != "" {
			key = strings.ToLower(a.Namespace) + ":" + key
		}
		switch {
		case strings.HasPrefix(key, "data-"), strings.HasPrefix(key, "on"), key == "style":
			continue
		case key == "srcset":
			a.Val = sanitizeSrcset(a.Val)
		case key == "poster" || key == "data" || key == "ping" || key == "cite" || key == "background":
			if scriptURL(a.Val) {
				continue
			}
			a.Val = SanitizeURL(a.Val)
		case key == "value" && sensitive:
			continue
		case key == "href" || key == "src" || key == "action" || key == "formaction" || key == "xlink:href":
			if scriptURL(a.Val) {
				continue
			}
			a.Val = SanitizeURL(a.Val)
		}
		kept = append(kept, a)
	}
	return kept
}
