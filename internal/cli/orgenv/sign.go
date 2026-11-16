package orgenv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgsign"
)

// Signed org definitions (#288, monomind#502). monomind 2.21 runs an org
// only when the operator signed its definition. mono-agent keeps that
// meaningful: a write it makes is re-signed only when the file verified
// before the write and the document came from exactly those bytes
// (orgsign.Before/After); everything else — an outside edit, an imported
// org, a chat assistant's change — is left for the operator to review and
// sign with `org sign`, which shows monomind's own review first.

// SigningOn reports whether the monomind that runs in a project root
// enforces signatures (its own handshake, run in that root).
// A var so tests can switch it without a monomind.
var SigningOn = monomind.OrgSigningEnforced

// MonomindSigner signs through monomind.
type MonomindSigner struct{}

func (MonomindSigner) Sign(ctx context.Context, root, org, hash string) error {
	_, err := monomind.OrgSign(ctx, root, org, hash)
	return err
}

// EnforcesHash: monomind's own --expect-hash (2.22) refuses other content.
func (MonomindSigner) EnforcesHash(ctx context.Context, root string) bool {
	return monomind.OrgSignExpectsHash(ctx, root)
}

// Check is monomind's own verdict where it has one (2.22 --check).
func (MonomindSigner) Check(ctx context.Context, root, org string) (orgsign.Status, bool) {
	return monomind.OrgSignCheck(ctx, root, org)
}

// SignStatus is org's signature state: monomind's own check where it
// has one, else the Go check.
func SignStatus(ctx context.Context, root, org string, raw []byte) orgsign.Status {
	if c, ok := Signer.(orgsign.Checker); ok {
		if st, ok := c.Check(ctx, root, org); ok {
			return st
		}
	}
	return orgsign.Verify(root, org, raw)
}

// SignCheckAll is monomind.OrgSignCheckAll; a var so tests can stand
// in for it.
var SignCheckAll = monomind.OrgSignCheckAll

// Signer signs through monomind; a var so tests can stand one in.
var Signer orgsign.Signer = MonomindSigner{}

// SaveSigned writes doc like orgdesign.Save and keeps its signature:
// from is the org file the document replaces (a rename's old name). A new
// org is never signed here — a whole document can come from anyone, the
// chat assistant included — so the user reviews it with `org sign`.
// The outcome is nil when monomind does not enforce signatures.
func SaveSigned(ctx context.Context, root, from string, doc *orgdesign.Doc) (string, *orgsign.Outcome, error) {
	if !SigningOn(ctx, root) {
		sha, err := orgdesign.Save(root, doc)
		return sha, nil, err
	}
	pre := orgsign.Before(ctx, Signer, root, from, doc.LoadedSHA(), false)
	sha, err := orgdesign.Save(root, doc)
	if err != nil {
		return "", nil, err
	}
	out := pre.After(ctx, Signer, doc.Name, sha)
	return sha, &out, nil
}

// SaveUnsigned writes doc like SaveSigned but never re-signs: for a caller that cannot tell who
// caused the change (the daemon, which has no agent marker or turn verdict, reconciling from rows a
// coder turn may have written). A still-verified org is left for the operator to review.
func SaveUnsigned(ctx context.Context, root string, doc *orgdesign.Doc, why string) (string, *orgsign.Outcome, error) {
	sha, err := orgdesign.Save(root, doc)
	if err != nil || !SigningOn(ctx, root) {
		return sha, nil, err
	}
	out := orgsign.Declined(root, doc.Name, why).After(ctx, Signer, doc.Name, sha)
	return sha, &out, nil
}

// DropSignature removes the signature of an org about to be replaced by a whole document, so the
// replacement can never be re-signed without the operator's review. Best effort: where the
// signature cannot be removed (a role's process), the changed file already fails verification.
func DropSignature(root, org string) { _ = orgsign.Withdraw(root, org) }

// SaveDoc is SaveSigned for an edit of an existing org, reporting on
// stderr when the org is left for the operator to review.
func SaveDoc(ctx context.Context, root string, doc *orgdesign.Doc) (string, error) {
	sha, out, err := SaveSigned(ctx, root, doc.Name, doc)
	WarnSignature(out)
	return sha, err
}

// WarnSignature prints out's notice, if any, as a warning on stderr.
func WarnSignature(out *orgsign.Outcome) {
	if out != nil && out.Notice != "" {
		fmt.Fprintln(os.Stderr, "warning: "+out.Notice)
	}
}

// WithSignature adds each org's signature state to `org status` JSON (a
// single {name,...} object or a {items:[...]} list) when monomind enforces
// signatures. The payload is returned unchanged on any decode problem.
func WithSignature(ctx context.Context, root string, payload json.RawMessage) json.RawMessage {
	if !SigningOn(ctx, root) {
		return payload
	}
	// One monomind run answers for every org (2.22 --check --all).
	all, haveAll := SignCheckAll(ctx, root)
	add := func(item map[string]interface{}) {
		name, _ := item["name"].(string)
		if !orgdesign.ValidOrgName(name) {
			return
		}
		var st orgsign.Status
		if got, ok := all[name]; haveAll && ok {
			st = got
		} else if raw, _, err := orgsign.ReadFile(root, name); err == nil {
			st = orgsign.Verify(root, name, raw)
		} else {
			return
		}
		item["signature"] = st
		// The daemon leaves an org it reconciled unsigned and only logs why; this is where the operator sees it.
		if !st.OK() {
			item["signature_notice"] = orgsign.Message(name, st)
		}
	}
	var obj map[string]interface{}
	if json.Unmarshal(payload, &obj) != nil {
		return payload
	}
	if items, ok := obj["items"].([]interface{}); ok {
		for _, it := range items {
			if m, ok := it.(map[string]interface{}); ok {
				add(m)
			}
		}
	} else {
		add(obj)
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return payload
	}
	return b
}
