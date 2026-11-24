package httpapi

import (
	"github.com/monoes/mono-agent/internal/tlsserve"
	"net"
	"net/http"
	"os"
	"strings"
)

// allowedHostsEnv lists extra Host header values (comma separated, host:port as a
// client sends them) the server answers to. It is the only way for a server bound to
// a non-loopback address to serve requests by that address or name.
const allowedHostsEnv = "MONOAGENT_HTTPAPI_ALLOWED_HOSTS"

// hostGuard answers 421 to every request whose Host header is not one of allowed,
// before routing, the account gate and any token check, so the answer says nothing
// about which routes or tokens exist. A web page can make a browser send requests
// to a loopback server under an attacker's own name; that name never matches.
func hostGuard(allowed map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowed[strings.ToLower(r.Host)] {
			writeError(w, http.StatusMisdirectedRequest, "unexpected Host header: to serve this name, set "+allowedHostsEnv+"=host:port,...")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowedHosts is the set of Host values for a server listening on bound: the
// loopback spellings of the bound port (always, for a loopback bind), the bound
// address itself, and the operator's configured extras. A wildcard or other
// non-loopback bind allows the loopback literals and the configured list only.
func allowedHosts(bound string, extra []string) map[string]bool {
	set := map[string]bool{}
	host, port, err := net.SplitHostPort(bound)
	if err == nil {
		for _, h := range []string{"127.0.0.1", "localhost", "::1"} {
			set[net.JoinHostPort(h, port)] = true
		}
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			set[net.JoinHostPort(host, port)] = true
		}
	}
	for _, h := range extra {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			set[h] = true
		}
	}
	return set
}

func configuredHosts(opts Options) []string {
	extra := append([]string(nil), opts.AllowedHosts...)
	if v := os.Getenv(allowedHostsEnv); v != "" {
		extra = append(extra, strings.Split(v, ",")...)
	}
	return extra
}

// nonLoopbackWarning is the start-up line for a server bound beyond loopback with no
// allowed hosts configured ("" otherwise).
func nonLoopbackWarning(bound string, extra []string) string {
	if tlsserve.IsLoopbackAddr(bound) || len(extra) > 0 {
		return ""
	}
	return "warning: the HTTP API is bound to " + bound + ", not loopback: requests addressed by a LAN IP or hostname " +
		"get 421 (unexpected Host header). Allow them with " + allowedHostsEnv + "=host:port,... (the loopback names of this port always work)"
}

// LoopbackHostGuard is hostGuard for another listener that serves its own routes (the
// dedicated /v1 listener): on a loopback bind only the loopback names of the bound port
// and the operator's MONOAGENT_HTTPAPI_ALLOWED_HOSTS get through, so a page that makes a
// browser reach the port under an attacker's name is turned away. A bind beyond loopback
// serves by its own address or name, so there next is returned as it is.
func LoopbackHostGuard(bound string, next http.Handler) http.Handler {
	if !tlsserve.IsLoopbackAddr(bound) {
		return next
	}
	return hostGuard(allowedHosts(bound, configuredHosts(Options{})), next)
}
