package crawlsite

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// A crawl follows URLs it was handed by a page it does not control, so
// every hop is attacker-influenced. Two things have to be true of every
// connection it makes: the host is one the crawl was told to walk, and the
// address behind that host is on the public internet. The scope check
// answers the first. This file answers the second, and it does it at the
// dial, which is the only place that sees the address DNS actually
// returned — a name that resolves to 169.254.169.254, or one that resolves
// differently the second time it is asked (DNS rebinding), gets past every
// check made on the URL alone.

// maxRedirects bounds a redirect chain. Ten is what net/http's default
// policy allows, and the point here is to re-check each hop, not to be
// stricter about how many there are.
const maxRedirects = 10

// reservedBlocks are the ranges net.IP's own predicates do not cover:
// carrier-grade NAT, IETF protocol assignments, benchmarking, and the
// reserved top of the address space. The rest — loopback, RFC1918 and
// unique-local, link-local, multicast, unspecified — is asked of the IP
// directly, so this list stays short enough to read.
var reservedBlocks = func() []*net.IPNet {
	var out []*net.IPNet
	for _, cidr := range []string{
		"100.64.0.0/10",   // RFC 6598 carrier-grade NAT
		"192.0.0.0/24",    // RFC 6890 IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // RFC 2544 benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserved
		"2001:db8::/32",   // documentation
		"64:ff9b::/96",    // RFC 6052 NAT64: embeds an IPv4 address
		"64:ff9b:1::/48",  // RFC 8215 local-use NAT64
		"2002::/16",       // 6to4: embeds an IPv4 address
		"2001::/32",       // Teredo: embeds an IPv4 address
	} {
		if _, block, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, block)
		}
	}
	return out
}()

// blockedReason names why an address is off limits, or "" if it is fine to
// dial. An IPv4-mapped IPv6 address is judged as the IPv4 address it is.
func blockedReason(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip == nil:
		return "unparseable"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsLoopback():
		return "loopback"
	case ip.IsPrivate():
		return "private-network"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "link-local"
	case ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return "multicast"
	}
	for _, block := range reservedBlocks {
		if block.Contains(ip) {
			return "reserved"
		}
	}
	return ""
}

// blockedAddressError is a refusal with two audiences. Error() is for the
// person running the crawl, reading their own terminal: it names the host,
// what it resolved to and the option that would allow it. Summary() is for
// anywhere the result might travel, and says none of that — see note().
type blockedAddressError struct {
	host   string
	ip     net.IP
	reason string
}

func (e *blockedAddressError) Error() string {
	return fmt.Sprintf("refusing to connect to %s (%s): %s addresses are not public, "+
		"and a crawl reaches them only with AllowPrivateHosts", e.host, e.ip, e.reason)
}

// Summary is the flat form: enough to know the crawl refused a hop and
// that no option is missing, and nothing about what is behind the name.
func (e *blockedAddressError) Summary() string {
	return "refused: that host resolves to an address a crawl may not reach"
}

func errBlockedAddress(host string, ip net.IP, reason string) error {
	return &blockedAddressError{host: host, ip: ip, reason: reason}
}

// checkDialAddress reports whether a "host:port" address may be dialled,
// for an address that is already an IP literal.
func checkDialAddress(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return nil // a name: resolved and checked by ipGuard.dial
	}
	if reason := blockedReason(ip); reason != "" {
		return errBlockedAddress(host, ip, reason)
	}
	return nil
}

// ipGuard is a DialContext that resolves the name itself, refuses the whole
// host if any answer is a private address, and then connects to an address
// it has checked rather than to the name — so the resolution that was
// checked is the resolution that is used.
type ipGuard struct {
	base     func(context.Context, string, string) (net.Conn, error)
	resolver *net.Resolver
}

func (g *ipGuard) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if reason := blockedReason(ip); reason != "" {
			return nil, errBlockedAddress(host, ip, reason)
		}
		return g.base(ctx, network, addr)
	}
	resolver := g.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	for _, a := range ips {
		if reason := blockedReason(a.IP); reason != "" {
			return nil, errBlockedAddress(host, a.IP, reason)
		}
	}
	var lastErr error
	for _, a := range ips {
		conn, derr := g.base(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if derr == nil {
			return conn, nil
		}
		lastErr = derr
	}
	return nil, lastErr
}

// checkHostAddresses resolves host and refuses it if any address behind it
// is private. It is the weaker half of the guard: the check and the
// connection are two separate resolutions, so it cannot close DNS
// rebinding the way dialling a checked address does. It is what is left
// when the dialling cannot be reached.
func checkHostAddresses(ctx context.Context, host string) error {
	host = strings.Trim(host, "[]")
	if host == "" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if reason := blockedReason(ip); reason != "" {
			return errBlockedAddress(host, ip, reason)
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	for _, a := range ips {
		if reason := blockedReason(a.IP); reason != "" {
			return errBlockedAddress(host, a.IP, reason)
		}
	}
	return nil
}

// guardedRoundTripper checks the host of every request instead of every
// dial. See checkHostAddresses for what that costs.
type guardedRoundTripper struct{ base http.RoundTripper }

func (g guardedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkHostAddresses(req.Context(), req.URL.Hostname()); err != nil {
		return nil, err
	}
	return g.base.RoundTrip(req)
}

// guardTransport returns rt with the address guard installed. Every request
// the crawl makes goes through it — pages and robots.txt alike — so there
// is one place the guard has to be right rather than one per call site.
func guardTransport(rt http.RoundTripper, timeout time.Duration) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	t, ok := rt.(*http.Transport)
	if !ok {
		// A RoundTripper of someone else's making: its dialling is not
		// reachable, so the host is checked before it is handed over.
		return guardedRoundTripper{base: rt}
	}
	clone := t.Clone()
	g := &ipGuard{base: clone.DialContext}
	if g.base == nil {
		g.base = (&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}).DialContext
	}
	clone.DialContext = g.dial
	return clone
}

// checkRedirect is the crawl's redirect policy. The default policy follows
// a Location header anywhere, which turns any crawled page into a request
// the site's author chose: the check that decides whether a URL is this
// crawl's business has to run on every hop, before the hop is made.
func (c *Crawler) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	switch strings.ToLower(req.URL.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("refusing redirect to unsupported scheme %q", req.URL.Scheme)
	}
	host := strings.ToLower(req.URL.Hostname())
	if !c.scope.allows(host) {
		return fmt.Errorf("refusing redirect out of scope to %s", req.URL.Redacted())
	}
	if !c.opts.AllowPrivateHosts {
		if err := checkDialAddress(req.URL.Host); err != nil {
			return err
		}
	}
	return nil
}
