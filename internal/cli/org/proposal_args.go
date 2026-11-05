package org

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/orgdecide"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/orgproposal"
	"github.com/monoes/mono-agent/internal/storage"
)

// What a proposal can ask for (#1477): a grant (`org grant add`) or a change of the autonomy level and its decider
// (`org autonomy set`). Approval none, level full and the policy have no field here, so no proposal can carry them,
// and an unknown field is refused, so none can be added by a rewritten row either. The arguments are stored in one
// canonical form with every default filled in, so that what the user confirms is what the preview says and not
// what the command would default to later.

// grantProposalArgs is the canonical form of a grant.add proposal.
type grantProposalArgs struct {
	Role           string `json:"role"`
	Automation     string `json:"automation"`
	Mode           string `json:"mode"`
	Wait           bool   `json:"wait"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Approval       string `json:"approval"`
	MaxCallsPerRun int    `json:"max_calls_per_run"`
	MaxCallsPerDay int    `json:"max_calls_per_day"`
	MaxOutputBytes int    `json:"max_output_bytes"`
}

// grantState is what a grant proposal is made against; its digest changes when any of it does.
type grantState struct {
	Role, Endpoint, WorkflowFound bool
	Workflow, Tier                string
	Existing                      *orggrant.Tool
	ExistingID, ExistingUpdated   string
	Org                           string // orgIdentity
}

// orgIdentity digests what makes this org this org: the org has no id of its own, so its goal, kind, roles with their
// types and reporting lines, and automations. A same-named org created anew differs here (what changes as a grant is
// added, or while the org runs, is not part of it).
func orgIdentity(doc *orgdesign.Doc) string {
	type role struct{ ID, Type, ReportsTo, Kind string }
	id := struct {
		Name, Goal, Kind string
		Roles            []role
		Automations      []orgdesign.AutomationRef
	}{Name: doc.Name, Goal: doc.Goal, Kind: doc.Kind, Automations: doc.Automations}
	for _, r := range doc.Roles {
		rt := ""
		if r.ReportsTo != nil {
			rt = *r.ReportsTo
		}
		id.Roles = append(id.Roles, role{r.ID, r.Type, rt, r.Kind})
	}
	return digestOf(id)
}

// grantProposalIn is what a caller may send: every field but role and automation is optional.
type grantProposalIn struct {
	Role           string `json:"role"`
	Automation     string `json:"automation"`
	Mode           string `json:"mode"`
	Wait           *bool  `json:"wait"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Approval       string `json:"approval"`
	MaxCallsPerRun int    `json:"max_calls_per_run"`
	MaxCallsPerDay int    `json:"max_calls_per_day"`
	MaxOutputBytes int    `json:"max_output_bytes"`
}

// autonomyProposalArgs is a autonomy.set proposal: only what it sets is present.
type autonomyProposalArgs struct {
	Level            string   `json:"level,omitempty"`
	Decider          string   `json:"decider,omitempty"`
	DeciderRuntime   string   `json:"decider_runtime,omitempty"`
	DeciderModel     string   `json:"decider_model,omitempty"`
	DeciderThreshold *float64 `json:"decider_threshold,omitempty"`
	OnDeciderFailure string   `json:"on_decider_failure,omitempty"`
}

// maxProposalText bounds each caller string of a proposal.
const maxProposalText = 128

// decodeStrict reads one JSON object into v, refusing unknown fields and anything after it.
func decodeStrict(raw string, v any) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("unexpected data after the arguments")
	}
	return nil
}

// proposalPlan is a proposal read against the live org: its canonical arguments, what it supersedes, what the user is
// shown, the digest of the state it is made against, and the command it runs.
type proposalPlan struct {
	ArgsJSON string
	Target   string
	Preview  string
	Digest   string
	// ReadErr is set when the live state could not be read: the digest is then a placeholder and says nothing about the
	// org, so a caller must return the error and not judge the proposal by it.
	ReadErr error
	// sub builds the sibling command that applies it, argv are its flags; the org is its one argument.
	sub  func(*orgEnv) *cobra.Command
	argv []string
}

func digestOf(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// planProposal reads verb and raw against the org. The digest is set whenever the arguments parse (so a caller can
// tell stale from invalid); the error says what is wrong with them now, and the other fields are set only without one.
func (env *orgEnv) planProposal(ctx context.Context, db *storage.Database, profileID string, doc *orgdesign.Doc, verb, raw string) (*proposalPlan, error) {
	if doc == nil {
		return &proposalPlan{Digest: "no-org"}, env.d.InvalidInput("the org does not exist")
	}
	switch verb {
	case orgproposal.VerbGrantAdd:
		return env.planGrant(ctx, db, profileID, doc, raw)
	case orgproposal.VerbAutonomySet:
		return env.planAutonomy(ctx, db, profileID, doc, raw)
	}
	return &proposalPlan{Digest: "unknown-verb"}, env.d.InvalidInput("verb %q is not one a proposal can carry: grant.add or autonomy.set", verb)
}

func (env *orgEnv) planGrant(ctx context.Context, db *storage.Database, profileID string, doc *orgdesign.Doc, raw string) (*proposalPlan, error) {
	var in grantProposalIn
	if err := decodeStrict(raw, &in); err != nil {
		return &proposalPlan{Digest: "unparsable-args"}, env.d.InvalidInput("the grant arguments are not valid: %v", err)
	}
	if len(in.Role) > maxProposalText || len(in.Automation) > maxProposalText || len(in.Mode) > maxProposalText || len(in.Approval) > maxProposalText {
		return &proposalPlan{Digest: "unparsable-args"}, env.d.InvalidInput("a value is longer than %d bytes", maxProposalText)
	}
	a := grantProposalArgs{Role: in.Role, Automation: in.Automation, Mode: in.Mode, Wait: true, TimeoutSeconds: in.TimeoutSeconds,
		Approval: in.Approval, MaxCallsPerRun: in.MaxCallsPerRun, MaxCallsPerDay: in.MaxCallsPerDay, MaxOutputBytes: in.MaxOutputBytes}
	if in.Wait != nil {
		a.Wait = *in.Wait
	}
	if a.Mode == "" {
		a.Mode = "run"
	}
	if a.Approval == "" {
		a.Approval = "required" // the command's own default is none for a workflow that sends nothing
	}
	for _, d := range []struct {
		v *int
		d int
	}{{&a.TimeoutSeconds, orggrant.DefaultTimeoutSeconds}, {&a.MaxCallsPerRun, orggrant.DefaultMaxCallsPerRun},
		{&a.MaxCallsPerDay, orggrant.DefaultMaxCallsPerDay}, {&a.MaxOutputBytes, orggrant.DefaultMaxOutputBytes}} {
		if *d.v == 0 {
			*d.v = d.d
		}
	}
	// The state the grant is made against, read before anything is judged.
	st := grantState{Org: orgIdentity(doc)}
	var tier string
	var wfName string
	var ref *orgdesign.AutomationRef
	var role *orgdesign.Role
	role, _ = doc.FindRole(a.Role)
	ref = doc.FindAutomation(a.Automation)
	st.Role, st.Endpoint = role != nil, role != nil && role.IsEndpoint()
	if ref != nil {
		st.Workflow = ref.WorkflowID
		// A store that cannot be read says nothing about the workflow: that is the read's error. A workflow that is not there,
		// or not this profile's, is a state like any other.
		if _, err := env.OpenWorkflowStore(db).GetWorkflow(ctx, ref.WorkflowID); err != nil {
			return &proposalPlan{Digest: "unreadable-state", ReadErr: err}, err
		}
		if wf, err := env.orgWorkflowIn(ctx, db, profileID, ref.WorkflowID); err == nil {
			tier, wfName, st.WorkflowFound = orggrant.GrantTier(orggrant.OutboundNodes(wf)), wf.Name, true
		}
	}
	st.Tier = tier
	var replaces bool
	{
		grants, err := orggrant.NewStore(db.DB).ListGrants(ctx, profileID, doc.Name, a.Role)
		if err != nil {
			return &proposalPlan{Digest: "unreadable-state", ReadErr: err}, err
		}
		{
			for _, g := range grants {
				if t := g.Automation(); t != nil && t.Alias == a.Automation {
					replaces, st.Existing, st.ExistingID, st.ExistingUpdated = true, t, g.ID, g.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z")
				}
			}
		}
	}
	p := &proposalPlan{Digest: digestOf(st)}
	switch {
	case a.Role == "" || a.Automation == "":
		return p, env.d.InvalidInput("a grant names a role and an automation")
	case role == nil:
		return p, env.d.InvalidInput("org %q has no role %q", doc.Name, a.Role)
	case role.IsEndpoint():
		return p, env.d.InvalidInput("role %q is an automation role; only agent roles hold grants", a.Role)
	case ref == nil:
		return p, env.d.InvalidInput("org %q has no automation %q", doc.Name, a.Automation)
	case a.Mode != "run" && a.Mode != "trigger" && a.Mode != "status":
		return p, env.d.InvalidInput("mode must be run, trigger, or status")
	case a.Approval != "required":
		return p, env.d.InvalidInput("a grant a proposal makes always asks first: approval none lets the role run the automation without asking, which is the user's to give with `org grant add` in their own terminal")
	case a.TimeoutSeconds < 0 || a.MaxCallsPerRun < 0 || a.MaxCallsPerDay < 0 || a.MaxOutputBytes < 0:
		return p, env.d.InvalidInput("limits cannot be negative")
	case !st.WorkflowFound:
		return p, env.d.InvalidInput("the workflow of automation %q cannot be read", a.Automation)
	}
	canon, _ := json.Marshal(a)
	p.ArgsJSON, p.Target = string(canon), a.Role+"/"+a.Automation
	wait := "returns when the run finishes"
	if !a.Wait {
		wait = "does not wait for the run"
	}
	replace := ""
	if replaces {
		replace = " It replaces the grant the role has for this automation now."
	}
	p.Preview = fmt.Sprintf("Let role %s of org %s call the automation %s (workflow %q) in mode %s: %s, timeout %ds, approval required (each call asks first), "+
		"at most %d calls per run and %d per day, output up to %d bytes. Risk tier of the workflow: %s.%s",
		a.Role, doc.Name, a.Automation, wfName, a.Mode, wait, a.TimeoutSeconds, a.MaxCallsPerRun, a.MaxCallsPerDay, a.MaxOutputBytes, tier, replace)
	p.sub = newOrgGrantAddCmd
	p.argv = []string{"--role", a.Role, "--automation", a.Automation, "--mode", a.Mode, "--wait=" + strconv.FormatBool(a.Wait),
		"--timeout", strconv.Itoa(a.TimeoutSeconds), "--approval", a.Approval, "--max-calls-per-run", strconv.Itoa(a.MaxCallsPerRun),
		"--max-calls-per-day", strconv.Itoa(a.MaxCallsPerDay), "--max-output-bytes", strconv.Itoa(a.MaxOutputBytes)}
	return p, nil
}

func (env *orgEnv) planAutonomy(ctx context.Context, db *storage.Database, profileID string, doc *orgdesign.Doc, raw string) (*proposalPlan, error) {
	var a autonomyProposalArgs
	if err := decodeStrict(raw, &a); err != nil {
		return &proposalPlan{Digest: "unparsable-args"}, env.d.InvalidInput("the autonomy arguments are not valid: %v", err)
	}
	live, err := orgdecide.NewStore(db.DB).Get(ctx, profileID, doc.Name)
	if err != nil {
		return &proposalPlan{Digest: "unreadable-state", ReadErr: err}, err
	}
	p := &proposalPlan{Digest: digestOf(struct {
		Live *orgdecide.Autonomy
		Org  string
	}{live, orgIdentity(doc)})}
	after := snapshotAutonomy(live)
	var parts []string
	for _, v := range []string{a.Level, a.Decider, a.DeciderRuntime, a.DeciderModel, a.OnDeciderFailure} {
		if len(v) > maxProposalText {
			return p, env.d.InvalidInput("a value is longer than %d bytes", maxProposalText)
		}
	}
	// Caller text is quoted in the preview, so it cannot pass for the sentence around it.
	set := func(what string, v any) {
		if s, ok := v.(string); ok {
			v = strconv.Quote(s)
		}
		parts = append(parts, fmt.Sprintf("%s %v", what, v))
	}
	if a.Level != "" {
		if a.Level == orgdesign.LevelFull {
			return p, env.d.InvalidInput("level full lets the org's decider handle everything with no person involved, which is the user's to choose in their own terminal")
		}
		if a.Level != orgdesign.LevelManual && a.Level != orgdesign.LevelMid {
			return p, env.d.InvalidInput("level must be manual or mid")
		}
		after.Level = a.Level
		set("level", a.Level)
	}
	if a.Decider != "" {
		after.Decider.Kind = a.Decider
		set("decider", a.Decider)
	}
	if a.DeciderRuntime != "" {
		after.Decider.Runtime = a.DeciderRuntime
		set("decider runtime", a.DeciderRuntime)
	}
	if a.DeciderModel != "" {
		after.Decider.Model = a.DeciderModel
		set("decider model", a.DeciderModel)
	}
	if t := a.DeciderThreshold; t != nil {
		if math.IsNaN(*t) || !(*t > 0 && *t <= 1) {
			return p, env.d.InvalidInput("decider_threshold %v must be in (0,1]", *t)
		}
		after.Decider.Threshold = *t
		set("decider threshold", *t)
	}
	if a.OnDeciderFailure != "" {
		after.OnDeciderFailure = a.OnDeciderFailure
		set("on decider failure", a.OnDeciderFailure)
	}
	if len(parts) == 0 {
		return p, env.d.InvalidInput("nothing to change: name a level, a decider, its runtime or model, a threshold or the failure rule")
	}
	if err := after.Validate(); err != nil {
		return p, env.d.InvalidInput("%v", err)
	}
	canon, _ := json.Marshal(a)
	p.ArgsJSON, p.Target = string(canon), "autonomy"
	p.Preview = fmt.Sprintf("Change the autonomy of org %s (now level %s, decider %s): %s.", doc.Name, live.Level, live.Decider.Kind, strings.Join(parts, ", "))
	if orgdesign.LevelRank(after.Level) > orgdesign.LevelRank(live.Level) {
		p.Preview += " This lets the org approve more without a person."
	}
	p.sub = newOrgAutonomySetCmd
	if a.Level != "" {
		p.argv = append(p.argv, "--level", a.Level)
	}
	if a.Decider != "" {
		p.argv = append(p.argv, "--decider", a.Decider)
	}
	if a.DeciderRuntime != "" {
		p.argv = append(p.argv, "--decider-runtime", a.DeciderRuntime)
	}
	if a.DeciderModel != "" {
		p.argv = append(p.argv, "--decider-model", a.DeciderModel)
	}
	if a.DeciderThreshold != nil {
		p.argv = append(p.argv, "--decider-threshold", strconv.FormatFloat(*a.DeciderThreshold, 'g', -1, 64))
	}
	if a.OnDeciderFailure != "" {
		p.argv = append(p.argv, "--on-decider-failure", a.OnDeciderFailure)
	}
	return p, nil
}
