package org

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/storage"
)

// grant.remove (#1477): a proposal to take a grant away, confirmed like the others by the operator, who runs the real
// `org grant remove`. `org grant remove` itself is open to everyone (it only narrows what a role can run), so this adds
// no authority and removes none: it is what the chat's revoke stores instead of running the command from a chat.

// revokeProposalArgs is the canonical form of a grant.remove proposal: it names the grant and nothing else.
type revokeProposalArgs struct {
	Role       string `json:"role"`
	Automation string `json:"automation"`
}

// revokeState is what a revoke is made against: the org it is, and the grant row as it is (its id and last update move
// when the grant is changed or given again, so a revoke of a grant the user did not see goes stale).
type revokeState struct {
	Org             string
	Existing        *orggrant.Tool
	ExistingID      string
	ExistingUpdated string
}

func (env *orgEnv) planRevoke(ctx context.Context, db *storage.Database, profileID string, doc *orgdesign.Doc, raw string) (*proposalPlan, error) {
	var a revokeProposalArgs
	if err := decodeStrict(raw, &a); err != nil {
		return &proposalPlan{Digest: "unparsable-args"}, env.d.InvalidInput("the revoke arguments are not valid: %v", err)
	}
	if len(a.Role) > maxProposalText || len(a.Automation) > maxProposalText {
		return &proposalPlan{Digest: "unparsable-args"}, env.d.InvalidInput("a value is longer than %d bytes", maxProposalText)
	}
	st := revokeState{Org: orgIdentity(doc)}
	grants, err := orggrant.NewStore(db.DB).ListGrants(ctx, profileID, doc.Name, a.Role)
	if err != nil {
		return &proposalPlan{Digest: "unreadable-state", ReadErr: err}, err
	}
	for _, g := range grants {
		if t := g.Automation(); t != nil && t.Alias == a.Automation {
			st.Existing, st.ExistingID, st.ExistingUpdated = t, g.ID, g.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z")
		}
	}
	p := &proposalPlan{Digest: digestOf(struct {
		Verb  string
		State revokeState
	}{"grant.remove", st})}
	if a.Role == "" || a.Automation == "" {
		return p, env.d.InvalidInput("a revoke names a role and an automation")
	}
	if st.Existing == nil {
		return p, env.d.InvalidInput("role %q of org %q has no grant for %q", a.Role, doc.Name, a.Automation)
	}
	canon, _ := json.Marshal(a)
	p.ArgsJSON, p.Target = string(canon), a.Role+"/"+a.Automation
	p.Preview = fmt.Sprintf("Revoke the grant that lets role %s of org %s call the automation %s (workflow %q, mode %s, approval %s). "+
		"The role can no longer call it; this takes effect at the next org start.",
		a.Role, doc.Name, a.Automation, st.Existing.WorkflowID, st.Existing.Mode, st.Existing.Approval)
	p.sub = newOrgGrantRemoveCmd
	p.argv = []string{"--role", a.Role, "--automation", a.Automation}
	return p, nil
}
