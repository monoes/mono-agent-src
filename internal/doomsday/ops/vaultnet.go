package ops

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// PinnedTransport is the Transport of vault.use (note 13.4 section 4A, network): the name is resolved once, every
// returned address must be public (unless the charter names a private range), only that pinned set is dialled with
// the host name as the TLS server name, proxy environment variables are ignored and a redirect is a refusal. A second
// resolution never happens.
type PinnedTransport struct {
	Resolver interface {
		LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
	}
	// Dial connects to an address; nil uses a net.Dialer.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// AllowPrivate lets the charter name private, link-local or metadata ranges; empty: public addresses only.
	AllowPrivate []netip.Prefix
	// TLSConfig is the client configuration (roots); nil is the system default. The server name is always the host.
	TLSConfig *tls.Config
}

// ErrRedirect: the destination answered with a redirect, which is refused (and audited by the caller).
var ErrRedirect = errors.New("doomsday ops: redirects are not followed")

// ErrNotPublic: a resolved address is not a public one.
var ErrNotPublic = errors.New("doomsday ops: the name resolves to an address that is not public")

// PublicAddr says whether a is a public unicast address, after normalising an IPv4-mapped IPv6 address to IPv4.
func PublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !a.IsLinkLocalMulticast() &&
		!a.IsMulticast() && !a.IsUnspecified() && !a.IsInterfaceLocalMulticast() && !cgnat.Contains(a)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func (p PinnedTransport) pinned(ctx context.Context, host string) ([]netip.Addr, error) {
	addrs, err := p.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return nil, fmt.Errorf("resolving: %w", errors.Join(err, errors.New("no address")))
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		a = a.Unmap()
		ok := PublicAddr(a)
		for _, pf := range p.AllowPrivate {
			ok = ok || pf.Contains(a)
		}
		if !ok {
			return nil, ErrNotPublic // one bad address refuses the lot: no picking the good one
		}
		out = append(out, a)
	}
	return out, nil
}

// transport dials only the pinned set, ignores proxy environment variables and keeps no connection.
func (p PinnedTransport) transport(addrs []netip.Addr) *http.Transport {
	dial := p.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	}
	return &http.Transport{
		Proxy: nil, // proxy environment variables are ignored
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var last error
			for _, a := range addrs { // only the pinned set, never a second lookup
				c, err := dial(ctx, network, net.JoinHostPort(a.String(), "443"))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
		DisableKeepAlives: true,
		TLSClientConfig:   p.TLSConfig,
	}
}

// Do implements Transport. The secret is placed at req.Inject: a header name, or "body:<field>" is not supported in
// this transport (the body is only ever its hash in the approval), so Inject names a header.
func (p PinnedTransport) Do(ctx context.Context, req VaultArgs, secret []byte) (Response, error) {
	u, err := url.Parse(req.URL)
	if err != nil || u.Scheme != "https" {
		return Response{}, ErrMalformedResponse
	}
	addrs, err := p.pinned(ctx, u.Hostname())
	if err != nil {
		return Response{}, err
	}
	client := &http.Client{Transport: p.transport(addrs), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	hr, err := http.NewRequestWithContext(ctx, req.Method, req.URL, nil)
	if err != nil {
		return Response{}, ErrMalformedResponse
	}
	for _, h := range req.Headers {
		hr.Header.Add(h[0], h[1])
	}
	hr.Header.Set(req.Inject, string(secret))
	resp, err := client.Do(hr)
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 && strings.TrimSpace(resp.Header.Get("Location")) != "" {
		resp.Body.Close()
		return Response{}, ErrRedirect
	}
	return Response{Status: resp.StatusCode, Body: resp.Body}, nil
}
