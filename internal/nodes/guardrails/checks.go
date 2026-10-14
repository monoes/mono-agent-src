package guardrails

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ── keywords ────────────────────────────────────────────────────────────

// matchKeywords returns each keyword found in text as a whole word (case
// insensitive), as written in text, once per keyword.
func matchKeywords(text string, keywords []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, kw := range keywords {
		kw = strings.TrimRight(strings.TrimSpace(kw), ".,!?;:")
		if kw == "" {
			continue
		}
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(kw))
		first, _ := utf8.DecodeRuneInString(kw)
		last, _ := utf8.DecodeLastRuneInString(kw)
		for _, m := range re.FindAllStringIndex(text, -1) {
			if isWordRune(first) {
				if r, _ := utf8.DecodeLastRuneInString(text[:m[0]]); m[0] > 0 && isWordRune(r) {
					continue
				}
			}
			if isWordRune(last) {
				if r, _ := utf8.DecodeRuneInString(text[m[1]:]); m[1] < len(text) && isWordRune(r) {
					continue
				}
			}
			v := text[m[0]:m[1]]
			if !seen[strings.ToLower(v)] {
				seen[strings.ToLower(v)] = true
				out = append(out, v)
			}
		}
	}
	return out
}

func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// ── custom regex ────────────────────────────────────────────────────────

// namedRegex is a custom_regex entry: matches are reported (and replaced
// with <name> by sanitize) under name.
type namedRegex struct {
	name string
	re   *regexp.Regexp
}

// regexLiteral accepts /pattern/flags (n8n's form) or a bare pattern.
var regexLiteral = regexp.MustCompile(`^/(.*)/([a-z]*)$`)

// compileRegex compiles a custom pattern. Of the JavaScript flags, i, m
// and s carry over; g, u and y need nothing.
func compileRegex(src string) (*regexp.Regexp, error) {
	pattern, flags := src, ""
	if m := regexLiteral.FindStringSubmatch(strings.TrimSpace(src)); m != nil {
		pattern, flags = m[1], m[2]
	}
	var goFlags string
	for _, f := range flags {
		switch f {
		case 'i', 'm', 's':
			goFlags += string(f)
		case 'g', 'u', 'y', 'd':
		default:
			return nil, fmt.Errorf("unknown flag %q", f)
		}
	}
	if goFlags != "" {
		pattern = "(?" + goFlags + ")" + pattern
	}
	return regexp.Compile(pattern)
}

func detectRegexes(text string, list []namedRegex) []finding {
	var out []finding
	for _, r := range list {
		for _, m := range r.re.FindAllStringIndex(text, -1) {
			if m[1] > m[0] {
				out = append(out, finding{kind: r.name, text: text[m[0]:m[1]], start: m[0], end: m[1]})
			}
		}
	}
	return out
}

// ── URLs ────────────────────────────────────────────────────────────────

type urlRules struct {
	allowed         []string
	schemes         []string
	blockUserinfo   bool
	allowSubdomains bool
}

var (
	schemeURLRe = regexp.MustCompile(`(?i)\b(?:https?|ftp)://[^\s<>"{}|\\^` + "`" + `\[\]]+|(?i)\b(?:data|javascript|vbscript|mailto):[^\s<>"{}|\\^` + "`" + `\[\]]+`)
	domainRe    = regexp.MustCompile(`\b(?:www\.)?[a-zA-Z0-9][a-zA-Z0-9-]*(?:\.[a-zA-Z0-9-]+)*\.[a-zA-Z]{2,}(?::\d+)?(?:/[^\s]*)?`)
	ipURLRe     = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?(?:/[^\s]*)?`)
	// fileExtTLD are file extensions that would otherwise read as a
	// scheme-less domain ("report.pdf").
	fileExtTLD = regexp.MustCompile(`(?i)\.(?:pdf|txt|docx?|xlsx?|pptx?|csv|json|xml|ya?ml|html?|css|js|ts|tsx|jsx|py|go|rb|php|java|cpp|png|jpe?g|gif|svg|zip|gz|tar|exe|log|ini|conf|toml|lock|md)$`)
)

const urlTrailing = ".,;:!?)]"

// detectURLs finds URLs with a scheme, scheme-less domains and IPs, each
// once; a domain already found inside a scheme URL is not repeated.
func detectURLs(text string) []string {
	var out []string
	seen := map[string]bool{}
	var claimed spanSet
	add := func(a, b int) {
		for b > a && strings.IndexByte(urlTrailing, text[b-1]) >= 0 {
			b--
		}
		if b <= a || claimed.overlaps(a, b) {
			return
		}
		claimed.add(a, b)
		if v := text[a:b]; !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, m := range schemeURLRe.FindAllStringIndex(text, -1) {
		add(m[0], m[1])
	}
	for _, m := range domainRe.FindAllStringIndex(text, -1) {
		v := text[m[0]:m[1]]
		if (m[0] > 0 && text[m[0]-1] == '@') || (m[1] < len(text) && text[m[1]] == '@') {
			continue // either half of an e-mail address
		}
		if !strings.Contains(v, "/") && !strings.HasPrefix(strings.ToLower(v), "www.") && fileExtTLD.MatchString(v) {
			continue
		}
		add(m[0], m[1])
	}
	for _, m := range ipURLRe.FindAllStringIndex(text, -1) {
		add(m[0], m[1])
	}
	return out
}

var hostlessSchemes = map[string]bool{"data": true, "javascript": true, "vbscript": true, "mailto": true}

// checkURL says why raw is blocked, or "" when it is allowed.
func (r urlRules) checkURL(raw string) string {
	scheme := "http"
	toParse := raw
	if i := strings.Index(raw, ":"); i > 0 {
		s := strings.ToLower(raw[:i])
		switch {
		case strings.HasPrefix(raw[i:], "://"):
			scheme = s
		case hostlessSchemes[s]:
			scheme = s
		default:
			toParse = "http://" + raw // host:port
		}
	} else {
		toParse = "http://" + raw
	}
	if !containsFold(r.schemes, scheme) {
		return "Blocked scheme: " + scheme
	}
	if hostlessSchemes[scheme] {
		return ""
	}
	u, err := url.Parse(toParse)
	if err != nil || u.Hostname() == "" {
		return "Invalid URL format"
	}
	if r.blockUserinfo && u.User != nil {
		return "Contains userinfo (potential credential injection)"
	}
	if !r.hostAllowed(u) {
		return "Not in allow list"
	}
	return ""
}

func (r urlRules) hostAllowed(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	for _, entry := range r.allowed {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "://") {
			a, err := url.Parse(entry)
			if err != nil || strings.ToLower(a.Hostname()) != host {
				continue
			}
			if a.Path == "" || a.Path == "/" || strings.HasPrefix(u.Path, a.Path) {
				return true
			}
			continue
		}
		if _, cidr, err := net.ParseCIDR(entry); err == nil {
			if ip := net.ParseIP(host); ip != nil && cidr.Contains(ip) {
				return true
			}
			continue
		}
		if net.ParseIP(entry) != nil {
			if entry == host {
				return true
			}
			continue
		}
		want := strings.TrimPrefix(entry, "www.")
		got := strings.TrimPrefix(host, "www.")
		if got == want || (r.allowSubdomains && strings.HasSuffix(got, "."+want)) {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(strings.TrimSpace(x), s) {
			return true
		}
	}
	return false
}

// ── sanitize ────────────────────────────────────────────────────────────

// applyMasks replaces every occurrence of each value with <TYPE>,
// longest values first so a value inside a longer one is not split.
func applyMasks(text string, masks map[string][]string) string {
	repl := map[string]string{}
	for kind, values := range masks {
		for _, v := range values {
			if v != "" {
				repl[v] = "<" + kind + ">"
			}
		}
	}
	keys := make([]string, 0, len(repl))
	for k := range repl {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		text = strings.ReplaceAll(text, k, repl[k])
	}
	return text
}
