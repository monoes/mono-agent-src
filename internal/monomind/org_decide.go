package monomind

import (
	"context"
	"encoding/json"
	"fmt"
)

// ResolveOptions attributes a resolution (capability
// org-decision-attribution, M5). On older monomind By is dropped and callers
// put "[decided by …]" into the resolution text instead; RequestID is refused.
type ResolveOptions struct {
	By        string // resolver, e.g. "model:claude-fable-5-1", "rule", "human"
	RequestID string // approvals only: resolve just this request
}

// attributionArgs returns --by when monomind supports it (M5).
func attributionArgs(ctx context.Context, opts ResolveOptions) []string {
	if opts.By == "" {
		return nil
	}
	caps, err := Capabilities(ctx)
	if err != nil || !caps.Has(CapOrgDecisionAttribution) {
		return nil
	}
	return []string{"--by", opts.By}
}

// resolveApproval runs `org approve|deny`. A RequestID is never dropped:
// without --request monomind resolves every pending request of (role, action),
// so a monomind that cannot take it is refused and nothing is sent.
func resolveApproval(ctx context.Context, projectRoot, verb, name, role, action string, opts ResolveOptions) (json.RawMessage, error) {
	args := append([]string{verb, name, role, action}, attributionArgs(ctx, opts)...)
	if opts.RequestID != "" {
		caps, err := Capabilities(ctx)
		if err != nil {
			return nil, err
		}
		if err := caps.Require(CapOrgDecisionAttribution, "resolving one approval request by its id"); err != nil {
			return nil, err
		}
		args = append(args, "--request", opts.RequestID)
	}
	return runOrgJSON(ctx, projectRoot, args...)
}

// OrgApproveWith approves a pending approval, attributed.
func OrgApproveWith(ctx context.Context, projectRoot, name, role, action string, opts ResolveOptions) (json.RawMessage, error) {
	return resolveApproval(ctx, projectRoot, "approve", name, role, action, opts)
}

// OrgDenyWith denies a pending approval, attributed.
func OrgDenyWith(ctx context.Context, projectRoot, name, role, action string, opts ResolveOptions) (json.RawMessage, error) {
	return resolveApproval(ctx, projectRoot, "deny", name, role, action, opts)
}

// OrgAnswerWith answers a pending ask_human question, attributed.
func OrgAnswerWith(ctx context.Context, projectRoot, name, questionID, answer string, opts ResolveOptions) (json.RawMessage, error) {
	cmd := append([]string{"answer"}, attributionArgs(ctx, opts)...)
	return runOrgJSONText(ctx, projectRoot, cmd, name, questionID, answer)
}

// orgDismissMinVersion is the monomind that has `org questions dismiss`
// (#572). It advertises no capability for it, so it is gated on the version.
const orgDismissMinVersion = "2.22.0"

// OrgDismissQuestionWith closes a pending ask_human question without an
// answer, attributed (--by). reason may be empty; it is passed as
// --reason=<text> so a leading dash is not read as a flag. Refused before
// anything runs when this monomind predates the command.
func OrgDismissQuestionWith(ctx context.Context, projectRoot, name, questionID, reason string, opts ResolveOptions) (json.RawMessage, error) {
	caps, err := Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	if !versionAtLeast(caps.Version, orgDismissMinVersion) {
		return nil, fmt.Errorf("monomind %s cannot dismiss questions (needs %s or newer)", caps.Version, orgDismissMinVersion)
	}
	cmd := []string{"questions", "dismiss"}
	if opts.By != "" {
		cmd = append(cmd, "--by", opts.By)
	}
	if reason != "" {
		cmd = append(cmd, "--reason="+reason)
	}
	return runOrgJSONText(ctx, projectRoot, cmd, name, questionID)
}

// OrgGateResolveWith approves or rejects a gate, attributed.
func OrgGateResolveWith(ctx context.Context, projectRoot, name, gateID string, approve bool, resolution string, opts ResolveOptions) (json.RawMessage, error) {
	sub := "gate-reject"
	if approve {
		sub = "gate-approve"
	}
	values := []string{name, gateID}
	if resolution != "" {
		values = append(values, resolution)
	}
	return runOrgJSONText(ctx, projectRoot, append([]string{sub}, attributionArgs(ctx, opts)...), values...)
}
