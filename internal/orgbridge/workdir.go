package orgbridge

import (
	"strings"

	"github.com/monoes/mono-agent/internal/orgdesign"
)

// senderWorkdir is the directory a message's sender is confined to in
// monomind, which an automation role's run gets as org.workdir so its file
// nodes confine to it (C-46): a role must not reach, through an automation
// role, files its own file tools cannot. from is "<role>" (a role of org)
// or "<org>:<role>".
//
//   - A role of an org under root: that role's workdir.
//   - A sender that resolves to no role: ok false, and senderRefused says
//     the delivery is dropped (everything but a person), so it never runs
//     wider than a role could and never runs unconfined.
//   - A person ("human", "operator:*"): no confinement (ok false), as for
//     any run a person starts.
func senderWorkdir(root, org, from string) (string, bool) {
	senderOrg, role := org, from
	if i := strings.IndexByte(from, ':'); i > 0 {
		senderOrg, role = from[:i], from[i+1:]
	}
	if root == "" {
		return "", false
	}
	if doc, err := orgdesign.Load(root, senderOrg); err == nil {
		if r, _ := doc.FindRole(role); r != nil {
			return orgdesign.RoleWorkdir(root, doc, role), true
		}
	}
	return "", false
}

// isPerson is the explicit allowlist of senders that are people, not roles.
func isPerson(from string) bool {
	return from == "human" || strings.HasPrefix(from, "operator:")
}

// senderRefused reports whether a delivery from from must be dropped: it is
// neither a resolvable role nor a person.
func senderRefused(root, org, from string) bool {
	if isPerson(from) {
		return false
	}
	_, ok := senderWorkdir(root, org, from)
	return !ok
}
