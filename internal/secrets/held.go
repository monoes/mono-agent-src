package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// A held send (internal/hilsend) keeps a reference to a secret it carries,
// never the value: a token the card guard mints, which the approval turns
// back into the value if the grant still holds. See the design of #1594.

type trackedForm struct {
	name string
	raw  bool // the value itself, not an encoding of it
}

// HeldNameOK reports whether name can be written into a held reference.
var heldNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// HeldNameOK is true for a vault entry name a held reference can carry.
func HeldNameOK(name string) bool { return heldNameRE.MatchString(name) }

// HeldToken is the reference to secret name with the given MAC.
func HeldToken(name, mac string) string { return "[[held-secret:" + name + ":" + mac + "]]" }

var heldTokenRE = regexp.MustCompile(`\[\[held-secret:([A-Za-z0-9._-]+):([0-9a-f]{32})\]\]`)

// ErrHeldRefused is wrapped by every refusal of ResolveHeld.
var ErrHeldRefused = errors.New("a held send's secret reference is not resolved")

func heldRefused(format string, a ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrHeldRefused, fmt.Sprintf(format, a...))
}

// ErrLiteralRef is wrapped by the refusal of a literal @secret: reference
// in a held send: only a reference the card guard minted resolves.
var ErrLiteralRef = errors.New("a held send holds a literal @secret: reference, which is never resolved at approval")

// HoldsTracked reports whether s holds a secret value (or a form of one)
// that ctx's run read under agent access.
func HoldsTracked(ctx context.Context, s string) bool {
	for _, v := range ResolvedValues(ctx) {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}

// UnresolveTracked replaces every secret value ctx's run read under agent
// access, wherever s holds it, by token(name). ok is false when s holds a
// derived form of a value (an encoding): it cannot be turned back into the
// value faithfully, so the caller must not store s. Outside agent access s is
// returned unchanged.
func UnresolveTracked(ctx context.Context, s string, token func(name string) string) (out string, ok bool) {
	a := accessOf(ctx)
	if a == nil {
		return s, true
	}
	vals := a.values()
	a.mu.Lock()
	forms := make(map[string]trackedForm, len(a.forms))
	for k, v := range a.forms {
		forms[k] = v
	}
	a.mu.Unlock()
	sort.SliceStable(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	for _, v := range vals {
		if !strings.Contains(s, v) {
			continue
		}
		f, known := forms[v]
		if !known || !f.raw || !HeldNameOK(f.name) {
			return s, false
		}
		s = strings.ReplaceAll(s, v, token(f.name))
	}
	return s, true
}

// GrantedValues are the values of the secrets agent access covers now (name
// -> value), for the scrub of rows that held one. Empty when there is no
// grant or nothing readable.
func GrantedValues(ctx context.Context, db *sql.DB, profileID string) map[string]string {
	g, err := GetAgentAccess(ctx, db, profileID)
	if err != nil || g == nil {
		return nil
	}
	names := g.Secrets
	if len(names) == 0 {
		entries, err := List(ctx, db, profileID)
		if err != nil {
			return nil
		}
		for _, e := range entries {
			names = append(names, e.Name)
		}
	}
	out := map[string]string{}
	for _, n := range names {
		if AgentCannotRead(n) {
			continue
		}
		if v, err := resolveEntry(ctx, db, profileID, secretRefPrefix+n, n); err == nil && len(v) >= MinTrackedLen {
			out[n] = v
		}
	}
	return out
}

var literalRefRE = regexp.MustCompile(`(?i)@\s*secret\s*:`)

// HasLiteralRef reports whether s holds a @secret: reference as text (any
// case, spaces inside, or URL-encoded).
func HasLiteralRef(s string) bool {
	if literalRefRE.MatchString(s) {
		return true
	}
	if strings.Contains(s, "%") {
		if u, err := url.QueryUnescape(s); err == nil && literalRefRE.MatchString(u) {
			return true
		}
		if u, err := url.PathUnescape(s); err == nil && literalRefRE.MatchString(u) {
			return true
		}
	}
	return false
}

// HeldOptions says what a held reference is resolved for.
type HeldOptions struct {
	// Verify tells whether the MAC belongs to this reference on this card.
	Verify func(name, mac string) bool
	// NodeType and Host are the send's: a host-bound grant covers a node
	// type that checks its destination, and the host is one the grant names.
	NodeType, Host string
	// Card is the held send's id, written to the audit.
	Card string
	// Dry checks without reading a value or writing an audit row.
	Dry bool
}

// ResolveHeld turns the references the card guard minted in s into their
// values, under the grant as it stands now. A reference that fails its MAC, a
// literal @secret: and any reference the grant no longer allows (turned off,
// expired, name not covered, host binding) are refused and nothing is
// resolved. Each read is audited by name.
func ResolveHeld(ctx context.Context, db *sql.DB, profileID, s string, o HeldOptions) (string, error) {
	if HasLiteralRef(s) {
		return s, ErrLiteralRef
	}
	locs := heldTokenRE.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		if strings.Contains(s, "[[held-secret:") {
			return s, heldRefused("a malformed secret reference")
		}
		return s, nil
	}
	if o.Verify == nil {
		return s, heldRefused("no card to check the reference against")
	}
	a := &accessState{db: db, profileID: profileID, origin: AccessOrigin{WorkflowID: "hil-approval", ExecutionID: o.Card},
		seen: map[string]struct{}{}, forms: map[string]trackedForm{}, names: map[string]struct{}{}, nodes: map[string]struct{}{}}
	ctx = context.WithValue(ctx, accessNodeKey{}, accessNode{id: "approval", name: "approval of " + o.NodeType, typ: o.NodeType})
	var b strings.Builder
	last := 0
	for _, m := range locs {
		name, mac := s[m[2]:m[3]], s[m[4]:m[5]]
		if !o.Verify(name, mac) {
			return s, heldRefused("the reference to %s was not made for this card", name)
		}
		if err := a.permit(ctx, secretRefPrefix+name, name); err != nil {
			return s, heldRefused("%s", err.Error())
		}
		g, err := GetAgentAccess(ctx, db, profileID)
		if err != nil || g == nil {
			return s, heldRefused("agent access was turned off or has expired")
		}
		if !g.AnyHost && !g.allowsHost(o.Host) {
			return s, heldRefused("%s may only go to the hosts agent access names, and this send's host is %q", name, HostOf(o.Host))
		}
		val := ""
		if !o.Dry {
			if val, err = resolveEntry(ctx, db, profileID, secretRefPrefix+name, name); err != nil {
				return s, heldRefused("%s could not be read: %v", name, err)
			}
			if err := a.resolved(ctx, name, val); err != nil {
				return s, heldRefused("%s", err.Error())
			}
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(val)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), nil
}
