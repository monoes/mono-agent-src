package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/secrets"
)

// The grant's host binding for every node type. A grant that names hosts
// (`secret agent-access on --host`) holds a secret read under it to those
// hosts, but only http.request, http.graphql, mcp.call and trigger.sse check
// the host they send to (secrets.CheckSendHost). Any other node type that
// names a destination of its own (an ssh or ftp host, a queue broker, an
// LDAP server, a database, a vendor's base URL) could be handed the value
// by an earlier step of the run, through the run's data, and send it there.
//
// CheckSendDestinations closes that for every executor that declares
// CredentialDestinations: each destination its config names is held to the
// grant before Execute. It is the same rule the http nodes apply at the
// send, so it can refuse a node that never read a secret itself: once any
// step of the run has read one, every send of the run is held to the grant's
// hosts (secrets.CheckSendHost, accessState.carries). Under no agent access
// (an approved workflow, no grant) and under a grant with no host binding
// (--any-host, or a legacy row with no binding) nothing changes.

// CheckSendDestinations refuses a node whose destination keys name a host
// the grant behind ctx does not, when its send would carry a secret. keys
// are the executor's CredentialDestinationKeys, resolved the config after
// expressions and @secret references.
func CheckSendDestinations(ctx context.Context, nodeType string, keys [][]string, resolved map[string]interface{}) error {
	// The http nodes and mcp.call check each hop at the send, and a held-back
	// URL of theirs is not yet a value here.
	if !secrets.UnderAgentAccess(ctx) || secrets.ChecksHostAtSend(nodeType) {
		return nil
	}
	var texts []string
	for _, group := range keys {
		for _, key := range group {
			if key == "credential_platform" { // names a vendor, not a host
				continue
			}
			for _, d := range sendDestinations(destinationValue(resolved, key)) {
				if texts == nil {
					texts = sendTexts(resolved, nil)
				}
				if err := secrets.CheckSendHost(ctx, d, texts...); err != nil {
					return fmt.Errorf("%q: %w", key, err)
				}
			}
		}
	}
	return nil
}

// destinationValue is the value at key of config, where a dotted key is a
// path into a nested block ("embedding.base_url").
func destinationValue(config map[string]interface{}, key string) interface{} {
	if v, ok := config[key]; ok {
		return v
	}
	block, rest, nested := strings.Cut(key, ".")
	if !nested {
		return nil
	}
	m, _ := config[block].(map[string]interface{})
	return destinationValue(m, rest)
}

// sendDestinations lists the hosts v names for a send: a list of bare hosts
// ("a:9092,b:9092") one by one, a port (digits) not at all, and a value that
// still holds an expression as it is, which names no host the grant has.
func sendDestinations(v interface{}) []string {
	if s, ok := v.(string); ok && strings.Contains(s, "{{") {
		return []string{s}
	}
	var out []string
	for _, d := range destinationsOf(v) {
		if strings.Trim(d, "0123456789") == "" {
			continue
		}
		out = append(out, d)
	}
	return out
}

// sendTexts appends every string value of v, at any depth, to out.
func sendTexts(v interface{}, out []string) []string {
	switch t := v.(type) {
	case string:
		out = append(out, t)
	case map[string]interface{}:
		for _, x := range t {
			out = sendTexts(x, out)
		}
	case []interface{}:
		for _, x := range t {
			out = sendTexts(x, out)
		}
	}
	return out
}
