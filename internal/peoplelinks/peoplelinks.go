// Package peoplelinks suggests cross-platform links between people rows that
// look like the same human (plan docs/plans/2026-09-25-jev-integration.md,
// WS8). Candidate pairs come from cheap Go-only signals (same normalised full
// name, same website, shared email or phone); only those pairs are sent to
// TypeSafe Jev, one noul request per pair (plan D13), and only when the
// profile enabled the people_links surface (D2). A confident "yes" becomes a
// suggested link for a human to confirm; people rows are never merged.
package peoplelinks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/net/publicsuffix"

	"github.com/monoes/mono-agent/internal/storage"
)

// DefaultLimit caps the pairs one run considers (plan: 200 per run).
const DefaultLimit = 200

// MaxBioChars caps the bio sent to Jev (runes).
const MaxBioChars = 1000

// DismissAt: a noul answer at or below this records the pair as a dismissed
// "not_same" link so it is never paid for again. Answers between DismissAt
// and the threshold record nothing and are asked again on a later run.
const DismissAt = 0.1

// QuestionID is the id of the one noul question in every request.
const QuestionID = "same_person"

// concurrency bounds in-flight Jev requests (plan D13: ≤8).
const concurrency = 4

// ErrDisabled is returned by Suggest when the profile has not run
// `monoagentcli jev enable people_links`.
var ErrDisabled = errors.New("the people_links surface is off for this profile (run: monoagentcli jev enable people_links)")

// Person is the subset of a people row the generator and the question use.
type Person struct {
	ID               string `json:"id"`
	Platform         string `json:"platform"`
	PlatformUsername string `json:"platform_username"`
	FullName         string `json:"full_name,omitempty"`
	Website          string `json:"website,omitempty"`
	ContactDetails   string `json:"-"`
	JobTitle         string `json:"job_title,omitempty"`
	Category         string `json:"category,omitempty"`
	Introduction     string `json:"-"`
}

// Pair is a candidate: two people of one profile on different platforms,
// A.ID < B.ID, with the signals that matched ("name", "website", "email",
// "phone").
type Pair struct {
	A       Person   `json:"a"`
	B       Person   `json:"b"`
	Reasons []string `json:"reasons"`
}

// ---------------------------------------------------------------------------
// Candidate generation (Go only)
// ---------------------------------------------------------------------------

// Candidates returns up to limit (≤0 ⇒ DefaultLimit) candidate pairs among
// the profile's people. Pairs on the same platform and pairs that already
// have a person_links row (any status) are skipped. Signals are taken in
// order of strength — shared email, shared phone, same website, same name —
// so the cap keeps the strongest pairs. The result is deterministic.
func Candidates(ctx context.Context, db *storage.Database, profileID string, limit int) ([]Pair, error) {
	if limit <= 0 {
		limit = DefaultLimit
	}
	people, err := loadPeople(ctx, db.DB, profileID)
	if err != nil {
		return nil, err
	}
	linked, err := linkedSet(db, profileID)
	if err != nil {
		return nil, err
	}

	type signal struct {
		name string
		keys func(Person) []string
	}
	signals := []signal{
		{"email", func(p Person) []string { return emails(p.ContactDetails) }},
		{"phone", func(p Person) []string { return phones(p.ContactDetails) }},
		{"website", func(p Person) []string { return nonEmpty(WebsiteKey(p.Website)) }},
		{"name", func(p Person) []string { return nonEmpty(NormalizeName(p.FullName)) }},
	}

	byPair := map[[2]int]*Pair{}
	var order [][2]int
scan:
	for _, sig := range signals {
		groups := map[string][]int{}
		for i, p := range people {
			for _, k := range sig.keys(p) {
				groups[k] = append(groups[k], i)
			}
		}
		keys := make([]string, 0, len(groups))
		for k, g := range groups {
			if len(g) > 1 {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			g := groups[k]
			for x := 0; x < len(g); x++ {
				for y := x + 1; y < len(g); y++ {
					i, j := g[x], g[y] // people is sorted by id, so i < j ⇒ A.ID < B.ID
					if strings.EqualFold(people[i].Platform, people[j].Platform) {
						continue
					}
					key := [2]int{i, j}
					if linked[people[i].ID+"\x00"+people[j].ID] {
						continue
					}
					if p, ok := byPair[key]; ok {
						if !contains(p.Reasons, sig.name) {
							p.Reasons = append(p.Reasons, sig.name)
						}
						continue
					}
					byPair[key] = &Pair{A: people[i], B: people[j], Reasons: []string{sig.name}}
					order = append(order, key)
					if len(order) >= limit {
						break scan
					}
				}
			}
		}
	}
	out := make([]Pair, len(order))
	for n, k := range order {
		out[n] = *byPair[k]
	}
	return out, nil
}

func loadPeople(ctx context.Context, db *sql.DB, profileID string) ([]Person, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(platform,''), COALESCE(platform_username,''),
		COALESCE(full_name,''), COALESCE(website,''), COALESCE(contact_details,''),
		COALESCE(job_title,''), COALESCE(category,''), COALESCE(introduction,'')
		FROM people WHERE profile_id = ? ORDER BY id`, profileID)
	if err != nil {
		return nil, fmt.Errorf("loading people: %w", err)
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Platform, &p.PlatformUsername, &p.FullName, &p.Website,
			&p.ContactDetails, &p.JobTitle, &p.Category, &p.Introduction); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func linkedSet(db *storage.Database, profileID string) (map[string]bool, error) {
	links, err := db.ListPersonLinks(profileID, "")
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(links))
	for _, l := range links {
		set[l.PersonA+"\x00"+l.PersonB] = true
	}
	return set, nil
}

// foldMap spells out letters that do not decompose into base + accent.
var foldMap = map[rune]string{
	'ß': "ss", 'æ': "ae", 'œ': "oe", 'ø': "o", 'ł': "l", 'đ': "d", 'ð': "d",
	'þ': "th", 'ı': "i", 'ħ': "h", 'ŀ': "l",
}

// accentBase maps accented Latin letters to their base letter.
var accentBase = func() map[rune]rune {
	m := map[rune]rune{}
	for base, accented := range map[rune]string{
		'a': "àáâãäåāăąǎ", 'c': "çćĉċč", 'd': "ď", 'e': "èéêëēĕėęě", 'g': "ĝğġģ",
		'h': "ĥ", 'i': "ìíîïĩīĭįǐ", 'j': "ĵ", 'k': "ķ", 'l': "ĺļľ", 'n': "ñńņňŉ",
		'o': "òóôõöōŏőǒ", 'r': "ŕŗř", 's': "śŝşšș", 't': "ţťț", 'u': "ùúûüũūŭůűųǔ",
		'w': "ŵ", 'y': "ýÿŷ", 'z': "źżž",
	} {
		for _, r := range accented {
			m[r] = base
		}
	}
	return m
}()

// NormalizeName lowercases name, strips accents, turns punctuation into
// spaces and collapses whitespace. Names with fewer than two tokens return ""
// (a single word is too weak a signal).
func NormalizeName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if s, ok := foldMap[r]; ok {
			b.WriteString(s)
			continue
		}
		if base, ok := accentBase[r]; ok {
			r = base
		}
		switch {
		case unicode.Is(unicode.Mn, r):
			// combining accent (already-decomposed input): drop
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	tokens := strings.Fields(b.String())
	if len(tokens) < 2 {
		return ""
	}
	return strings.Join(tokens, " ")
}

// sharedHosts are sites whose domain says nothing about who owns a page
// (social networks, link hubs, code and blog hosts). They never match on the
// domain alone; a website on one of them matches only when the whole profile
// address — host and path, e.g. "github.com/alice" or "linktr.ee/alice", or a
// user subdomain like "alice.substack.com" — is the same. GitHub is treated
// this way too: a shared github.com/<user> is a real signal, a shared
// github.com is not.
var sharedHosts = map[string]bool{
	"linktr.ee": true, "instagram.com": true, "linkedin.com": true, "x.com": true,
	"twitter.com": true, "tiktok.com": true, "facebook.com": true, "fb.com": true,
	"youtube.com": true, "youtu.be": true, "github.com": true, "github.io": true,
	"gitlab.com": true, "medium.com": true, "substack.com": true, "threads.net": true,
	"bsky.app": true, "beacons.ai": true, "bio.link": true, "linkin.bio": true,
	"carrd.co": true, "about.me": true, "wordpress.com": true, "blogspot.com": true,
	"google.com": true, "sites.google.com": true, "notion.site": true, "t.me": true,
	"wa.me": true, "bit.ly": true, "t.co": true, "pinterest.com": true, "reddit.com": true,
	"twitch.tv": true, "behance.net": true, "dribbble.com": true, "vimeo.com": true,
	"soundcloud.com": true, "spotify.com": true, "patreon.com": true, "ko-fi.com": true,
	"buymeacoffee.com": true, "calendly.com": true, "wix.com": true, "wixsite.com": true,
	"squarespace.com": true, "vercel.app": true, "netlify.app": true, "gmail.com": true,
}

// WebsiteKey returns the matching key for a website: its registrable domain
// ("blog.alice.co.uk" → "alice.co.uk"), or for a shared host the host (minus
// "www.") plus path; "" when the site gives no signal.
func WebsiteKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t\n") {
		return ""
	}
	if !strings.Contains(raw, "://") {
		if strings.Contains(raw, ":") {
			return "" // mailto:, tel:, …
		}
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == "" || !strings.Contains(host, ".") {
		return ""
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return ""
	}
	if !sharedHosts[domain] && !sharedHosts[host] {
		return domain
	}
	path := strings.Trim(strings.ToLower(u.EscapedPath()), "/")
	if host == domain && path == "" {
		return "" // bare shared host
	}
	if host != domain {
		return host // user subdomain, e.g. alice.substack.com
	}
	return host + "/" + path
}

var (
	emailRe = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`)
	phoneRe = regexp.MustCompile(`\+?\d[\d\s().\-]{6,}\d`)
)

func emails(s string) []string {
	var out []string
	for _, m := range emailRe.FindAllString(s, -1) {
		out = appendUnique(out, strings.ToLower(m))
	}
	return out
}

// phones keys phone numbers on their last 10 digits (so "+1 555 010 9999"
// and "(555) 010-9999" match); numbers under 9 digits are ignored.
func phones(s string) []string {
	var out []string
	for _, m := range phoneRe.FindAllString(s, -1) {
		var d strings.Builder
		for _, r := range m {
			if r >= '0' && r <= '9' {
				d.WriteRune(r)
			}
		}
		digits := d.String()
		if len(digits) < 9 {
			continue
		}
		if len(digits) > 10 {
			digits = digits[len(digits)-10:]
		}
		out = appendUnique(out, digits)
	}
	return out
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

func appendUnique(list []string, s string) []string {
	if contains(list, s) {
		return list
	}
	return append(list, s)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
