package mcpnode

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"strings"

	"github.com/monoes/mono-agent/internal/secrets"
)

// loopbackURL reports whether rawURL names this machine (localhost or a
// loopback address).
func loopbackURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	h := u.Hostname()
	if strings.EqualFold(strings.TrimSuffix(h, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// checkArgsContent holds a tool call's arguments, which are content, to the
// rule for a request's body: a granted secret reaches a remote server only
// as its headers (issue #1418). A server on this machine is exempt, and a
// stdio server is a program on it (an unapproved run may not start one).
func checkArgsContent(ctx context.Context, srv *serverConfig, args map[string]interface{}) error {
	if srv.Transport != "http" || !secrets.UnderAgentAccess(ctx) {
		return nil
	}
	raw, _ := json.Marshal(args)
	return secrets.CheckSendContent(ctx, loopbackURL(srv.URL), "tool arguments", string(raw))
}
