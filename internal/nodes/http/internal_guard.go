package httpnodes

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// internalNets are the ranges net.IP's own predicates miss: carrier-grade NAT, "this network",
// NAT64, and the documentation/benchmark blocks that are never a public server.
var internalNets = func() []*net.IPNet {
	var nets []*net.IPNet
	for _, c := range []string{"100.64.0.0/10", "0.0.0.0/8", "64:ff9b::/96", "198.18.0.0/15", "fc00::/7"} {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	return nets
}()

// isInternalIP is true for loopback, private, link-local (the cloud metadata address 169.254.169.254
// among them), unspecified and multicast addresses, and the ranges in internalNets.
func isInternalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	for _, n := range internalNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// refuseInternalDial is a DialContext for a request that an unapproved, AI-authored workflow makes:
// it connects only to addresses that are not internal. The check runs in the dialer's Control hook,
// on the address actually connected to, so a name that resolves differently for a check and for the
// dial (DNS rebinding) gets no further. The proxy, which the user configured, is dialled freely.
func refuseInternalDial(proxy *url.URL) func(context.Context, string, string) (net.Conn, error) {
	free := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	guarded := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := parseIP(host); ip == nil || isInternalIP(ip) {
				return fmt.Errorf("%s is an internal address (this computer, a private network or the cloud metadata service); "+
					"a workflow that an agent wrote and you have not approved does not reach it: save or activate the workflow to approve it", address)
			}
			return nil
		}}
	proxyAddr := ""
	if proxy != nil {
		port := proxy.Port()
		if port == "" {
			port = "80"
			if strings.EqualFold(proxy.Scheme, "https") {
				port = "443"
			}
		}
		proxyAddr = net.JoinHostPort(proxy.Hostname(), port)
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if proxyAddr != "" && strings.EqualFold(addr, proxyAddr) {
			return free.DialContext(ctx, network, addr)
		}
		return guarded.DialContext(ctx, network, addr)
	}
}
