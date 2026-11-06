package org

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// What the chat tools call to propose (#1477): the same planning, bounds and store as `org proposal create`, in this
// process and not through a child (a child run by the chat is marked as the chat and says nothing the gate has not
// already said). It only stores a pending row; applying one is the operator-only confirm.

// ProposalRequest is one proposal to store.
type ProposalRequest struct {
	DB        *storage.Database
	ProfileID string
	// Root is the org project root of the profile.
	Root string
	// OpenWorkflowStore opens the store the automation's workflow is read from.
	OpenWorkflowStore func(*storage.Database) *workflow.HybridWorkflowStore
	Org               string
	// Verb is grant.add or autonomy.set; ArgsJSON their arguments as `org proposal create --args-json` takes them.
	Verb, ArgsJSON string
	// Conversation is the label the proposal is made under (never ownership).
	Conversation string
}

// CreateProposal stores a pending proposal and returns it as `org proposal create` prints it (the "proposal" object).
func CreateProposal(ctx context.Context, r ProposalRequest) (map[string]interface{}, error) {
	if r.DB == nil || r.ProfileID == "" || r.Root == "" || r.OpenWorkflowStore == nil {
		return nil, errors.New("proposals are unavailable in this session")
	}
	// Who made it comes from the gate's verdict and is never the caller's to name. This entry point is the chat's, which
	// is an agent whatever the process around it looks like, so it is never labelled "operator".
	by := proposedBy()
	if by == "operator" {
		by = "chat"
	}
	plain := func(format string, a ...interface{}) error { return fmt.Errorf(format, a...) }
	env := newEnv(Deps{OpenWorkflowStore: r.OpenWorkflowStore, InvalidInput: plain, NotFound: plain},
		orgenv.New(orgenv.Deps{OpenWorkflowStore: r.OpenWorkflowStore}))
	doc, err := orgdesign.Load(r.Root, r.Org)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("org %q not found", r.Org)
		}
		return nil, err
	}
	return env.createProposal(ctx, r.DB, r.ProfileID, r.Root, doc, r.Verb, r.ArgsJSON, r.Conversation, by)
}
