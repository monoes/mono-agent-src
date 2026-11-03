package ops

import (
	"errors"
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
	"golang.org/x/text/unicode/norm"
)

// One canonicalisation function per field type (note 13.4 section 2, T19). Entry, approval and effect all call these
// and nothing else compares a raw string.

// Any is the explicit "unconstrained" value of a list constraint; the entry dialog renders it in plain words.
const Any = "*"

var (
	ErrBadHost        = errors.New("doomsday ops: not a valid host name")
	ErrBadPath        = errors.New("doomsday ops: not a valid path")
	ErrBadHandle      = errors.New("doomsday ops: not a valid handle or address")
	ErrNoPlatformRule = errors.New("doomsday ops: no handle rule for that platform, so it is not enabled")
	ErrBadInteger     = errors.New("doomsday ops: not a plain integer")
)

// CanonHost is a host name in its one canonical form: lowercase ASCII (punycode), no trailing dot. A port, a
// userinfo, a path, a space, a NUL or an IP literal is refused (an IP is never a host of a constraint).
func CanonHost(s string) (string, error) {
	if s == "" || strings.ContainsAny(s, "@/\\:?#[] \t\r\n\x00%") {
		return "", fmt.Errorf("%w: %q", ErrBadHost, s)
	}
	s = strings.TrimSuffix(s, ".")
	a, err := idna.Lookup.ToASCII(norm.NFKC.String(s))
	if err != nil || a == "" || strings.HasSuffix(a, ".") || strings.Contains(a, "..") || len(a) > 253 {
		return "", fmt.Errorf("%w: %q", ErrBadHost, s)
	}
	if _, err := netip.ParseAddr(a); err == nil {
		return "", fmt.Errorf("%w: an address is not a host name", ErrBadHost)
	}
	return strings.ToLower(a), nil
}

// HostAllowed says whether host (any form) is named by the allowed list: exactly, or by a registrable-domain
// suffix on a label boundary. The suffix rule applies only to an entry that is a registrable domain under the public
// suffix list including its private section (so a name on a shared host is not "the same domain" as another tenant);
// an entry the list does not know, or that is itself a public suffix, matches exactly only. Any matches every host.
func HostAllowed(allowed []string, host string) bool {
	h, err := CanonHost(host)
	if err != nil {
		return false
	}
	for _, e := range allowed {
		if e == Any {
			return true
		}
		c, err := CanonHost(e)
		if err != nil {
			continue
		}
		if h == c || (suffixMatchable(c) && strings.HasSuffix(h, "."+c)) {
			return true
		}
	}
	return false
}

func suffixMatchable(d string) bool {
	ps, icann := publicsuffix.PublicSuffix(d)
	if !icann && !strings.Contains(ps, ".") {
		return false // a name the list does not know
	}
	reg, err := publicsuffix.EffectiveTLDPlusOne(d)
	return err == nil && reg == d
}

// ParseInt64 parses a plain decimal integer: no sign other than '-', no '+', no fraction, exponent, NaN, Inf or
// space, and nothing that does not fit int64. It is how a charter's numeric fields and micro-dollar amounts enter.
func ParseInt64(raw string) (int64, error) {
	if !plainInt.MatchString(raw) {
		return 0, fmt.Errorf("%w: %q", ErrBadInteger, raw)
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is out of range", ErrBadInteger, raw)
	}
	return n, nil
}

var plainInt = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

// CanonRelPath is a path relative to a root, in NFC, slash-separated and clean. An absolute path, a ".." step, a
// NUL, an empty path or "." is refused. (Whether the target really lies beneath the root is decided on the open
// handle at the effect, never on this string.)
func CanonRelPath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", fmt.Errorf("%w: %q", ErrBadPath, p)
	}
	c := path.Clean(norm.NFC.String(p))
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("%w: %q", ErrBadPath, p)
	}
	return c, nil
}

var (
	handleX    = regexp.MustCompile(`^[a-z0-9_]{1,15}$`)
	handleInst = regexp.MustCompile(`^[a-z0-9._]{1,30}$`)
	emailLocal = regexp.MustCompile(`^[a-z0-9.!#$%&'*+/=?^_{|}~-]{1,64}$`)
)

// HasPlatformRule says whether CanonHandle knows the platform; one it does not know stays off.
func HasPlatformRule(platform string) bool {
	return slices.Contains([]string{"x", "instagram", "email"}, platform)
}

// CanonHandle is a handle or address in the platform's canonical form. A platform without a rule here is not
// enabled: ErrNoPlatformRule.
func CanonHandle(platform, h string) (string, error) {
	h = strings.ToLower(norm.NFKC.String(strings.TrimSpace(h)))
	bad := fmt.Errorf("%w: %q on %s", ErrBadHandle, h, platform)
	switch platform {
	case "x":
		h = strings.TrimPrefix(h, "@")
		if !handleX.MatchString(h) {
			return "", bad
		}
	case "instagram":
		h = strings.TrimPrefix(h, "@")
		if !handleInst.MatchString(h) || strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") {
			return "", bad
		}
	case "email":
		local, domain, ok := strings.Cut(h, "@")
		if !ok || strings.Contains(domain, "@") || !emailLocal.MatchString(local) {
			return "", bad
		}
		d, err := CanonHost(domain)
		if err != nil {
			return "", bad
		}
		h = local + "@" + d
	default:
		return "", fmt.Errorf("%w: %q", ErrNoPlatformRule, platform)
	}
	return h, nil
}
