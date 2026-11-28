package monomind

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Optional capabilities the org × workflow unification builds on
// (docs/plans/2026-09-16-org-unification-contracts.md §1). Unlike
// RequiredCapabilities, a missing one only disables the feature that needs
// it.
const (
	CapOrgToolProviders       = "org-tool-providers"       // M1 + M3: grants, boss/parent deciders, live org send
	CapOrgEndpointRoles       = "org-endpoint-roles"       // M2: automation roles
	CapOrgFederation          = "org-federation"           // M4: cross-root restrictions enforced
	CapOrgDecisionAttribution = "org-decision-attribution" // M5: --by, request-scoped approvals

	// Coder mode (monomind#355-#358): a full-access chat turn needs all four.
	CapAgentExecFullAccess   = "agent-exec-full-access"
	CapAgentExecSettings     = "agent-exec-settings"
	CapAgentExecToolActivity = "agent-exec-tool-activity"
	CapInitJSON              = "init-json"

	// Coder mode on every runtime: --access full for any runtime whose
	// scan entry has full_access, and `agent exec --effort`.
	CapAgentExecFullAccessAny = "agent-exec-full-access-any"
	CapAgentExecEffort        = "agent-exec-effort"

	// Dynamic org (monoes/mono-agent#226): stdio caller tools alongside
	// --access full (the lead's org_* tools, monomind#389), --access read
	// (research workers, monomind#388), and Claude's subagent lifecycle
	// events (monomind#387).
	CapAgentExecFullAccessTools = "agent-exec-full-access-tools"
	CapAgentExecAccessRead      = "agent-exec-access-read"
	CapAgentExecSubagentEvents  = "agent-exec-subagent-events"

	// Monomind 2.24: `--sandbox-fallback` on exec and test, `agent test
	// --sandbox`, an unknown cost as null (never 0), and agent models
	// entries that keep an alias with its target.
	CapAgentExecSandboxFallback = "agent-exec-sandbox-fallback"
	CapAgentTestSandbox         = "agent-test-sandbox"
	CapAgentExecCostNull        = "agent-exec-cost-null"
	CapAgentModelsAliasOf       = "agent-models-alias-of"

	// `agent exec --attach FILE`: files handed to the model with the prompt
	// (claude as image/document blocks, codex as -i, other runtimes as paths
	// in the prompt).
	CapAgentExecAttachments = "agent-exec-attachments"

	// `agent exec`'s cost_usd covers this turn only, also on a resumed claude
	// session, which before reported the session's running total (protocol rev
	// 35, monomind#860).
	CapAgentExecTurnCost = "agent-exec-turn-cost"
)

// ErrFeatureNeedsMonomind reports that the installed monomind lacks a
// capability a feature needs.
type ErrFeatureNeedsMonomind struct {
	Capability string
	Feature    string
	Installed  string // installed version, "" when unknown
}

func (e *ErrFeatureNeedsMonomind) Error() string {
	installed := e.Installed
	if installed == "" {
		installed = "unknown"
	}
	return fmt.Sprintf("%s needs monomind with capability %q (installed: %s) — update monomind: `npm install -g @monoes/monomindcli@latest`",
		e.Feature, e.Capability, installed)
}

// CapabilitySet is the result of one handshake.
type CapabilitySet struct {
	Version string
	caps    map[string]bool
}

// Has reports whether cap was advertised.
// NewCapabilitySet builds a set directly, for callers that stand in for a
// handshake (tests).
func NewCapabilitySet(version string, caps ...string) *CapabilitySet {
	set := &CapabilitySet{Version: version, caps: map[string]bool{}}
	for _, c := range caps {
		set.caps[c] = true
	}
	return set
}

func (c *CapabilitySet) Has(cap string) bool { return c != nil && c.caps[cap] }

// List returns the advertised capabilities.
func (c *CapabilitySet) List() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.caps))
	for k := range c.caps {
		out = append(out, k)
	}
	return out
}

// Require returns ErrFeatureNeedsMonomind when cap is missing.
func (c *CapabilitySet) Require(cap, feature string) error {
	if c.Has(cap) {
		return nil
	}
	v := ""
	if c != nil {
		v = c.Version
	}
	return &ErrFeatureNeedsMonomind{Capability: cap, Feature: feature, Installed: v}
}

// capabilityTTL bounds how long a handshake result is reused inside one
// long-running process (the daemon), so a monomind upgrade is noticed
// without a restart.
const capabilityTTL = 5 * time.Minute

var capCache struct {
	sync.Mutex
	set *CapabilitySet
	at  time.Time
}

// Capabilities runs (or reuses a recent) handshake and returns the
// advertised capability set. An error means monomind is missing or
// unusable.
func Capabilities(ctx context.Context) (*CapabilitySet, error) {
	capCache.Lock()
	defer capCache.Unlock()
	if capCache.set != nil && time.Since(capCache.at) < capabilityTTL {
		return capCache.set, nil
	}
	_, vi, err := Ensure(ctx)
	if err != nil {
		return nil, err
	}
	set := &CapabilitySet{Version: vi.Version, caps: map[string]bool{}}
	for _, c := range vi.Capabilities {
		set.caps[c] = true
	}
	capCache.set, capCache.at = set, time.Now()
	return set, nil
}

// ResetCapabilityCache drops the cached handshake (tests, and after a
// monomind upgrade is detected).
func ResetCapabilityCache() {
	capCache.Lock()
	capCache.set = nil
	capCache.Unlock()
	binCaps.Lock()
	binCaps.sets, binCaps.at = nil, nil
	binCaps.Unlock()
	resetSignTools()
	resetPins()
}
