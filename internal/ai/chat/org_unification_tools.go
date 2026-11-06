package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgproposal"
)

// Org × workflow unification tools (plan §7.1, §7.7). Each shells back into
// `monoagentcli org …` — the same code path the CLI and the GUI use — so
// grant rows, reconcile, and autonomy rules live in one place. Granting a
// role an automation or raising an org's autonomy hands agents new power,
// so both refuse once untrusted synced communications entered the session.
// Both are the user's to do (class B: `org grant add` and `org autonomy set` refuse
// the chat's marker), so they only PROPOSE: the tool stores a pending proposal
// (orgproposal, through the same planning as `org proposal create`) and applies nothing;
// the user confirms it on the card in the org screen. The proposal is only as strong
// as the courtesy gate that tells the chat from the user. Revoking a grant narrows
// access and still previews, then shells `org grant remove` on confirm:true.

func orgUnificationToolDefs(def func(name, desc string, props map[string]interface{}, required []string) ToolDef) []ToolDef {
	return []ToolDef{
		def("add_org_automation", "Add an existing workflow to an org as one of its automations, under a short alias (lowercase letters, digits, underscores). Roles can then be granted it as a tool with set_org_grant, or it can become an automation role.", map[string]interface{}{
			"org_name":    strParam("The org's name"),
			"workflow_id": strParam("The workflow to add (same profile)"),
			"alias":       strParam("Name the org uses for it; becomes the tool automation_<alias>"),
			"owned":       boolParam("Whether this org owns the workflow's lifecycle (at most one org may)"),
		}, []string{"org_name", "workflow_id", "alias"}),
		def("set_org_grant", "Grant (or with revoke:true, revoke) one org role the right to call one of the org's automations as a tool. Workflows with outbound nodes (email, chat, social, service writes) default to needing a decision per call. This only PROPOSES the change: it stores a pending proposal and applies nothing, and the result carries the proposal_id; tell the user to confirm (or decline) the proposal in the org screen, which is the only place it takes effect, and that it expires after 30 minutes. Revoking (revoke:true) previews, then applies with confirm:true.", map[string]interface{}{
			"org_name":          strParam("The org's name"),
			"role_id":           strParam("The agent role receiving the tool"),
			"automation":        strParam("The automation's alias in this org"),
			"mode":              strParam("run (default) | trigger | status"),
			"wait":              boolParam("Wait for the run and return its output (default true)"),
			"timeout_seconds":   intParam("Seconds to wait when wait is true (default 600)"),
			"approval":          strParam("required (the default) makes every call a decision routed by the org's autonomy level; none is the user's to give and is refused here"),
			"max_calls_per_run": intParam("Calls allowed per org run (default 20)"),
			"revoke":            boolParam("Revoke the grant instead of setting it"),
			"confirm":           boolParam("Only for revoke:true: must be true to apply; omit to preview"),
		}, []string{"org_name", "role_id", "automation"}),
		def("set_org_autonomy", "Change who decides an org's pending approvals, questions, and gates: level manual (a human decides everything), mid (rules approve routine items, the decider handles consequential ones, a human handles irreversible ones), or full (the decider handles everything, no human: only the user sets it, in their own terminal or the app). This only PROPOSES the change: it stores a pending proposal and applies nothing, and the result carries the proposal_id; tell the user to confirm (or decline) the proposal in the org screen, which is the only place it takes effect, and that it expires after 30 minutes.", map[string]interface{}{
			"org_name":           strParam("The org's name"),
			"level":              strParam("manual | mid (full is the user's to set and is refused here)"),
			"decider":            strParam(strings.Join(orgdesign.DeciderKinds, " | ") + " (jev: TypeSafe Jev picks the verdict; the model decider handles questions and anything Jev is unsure of)"),
			"decider_threshold":  map[string]interface{}{"type": "number", "description": "jev decider only: lowest top-verdict probability Jev decides at, in (0,1] (default 0.8)"},
			"decider_model":      strParam("Model id for the model decider"),
			"decider_runtime":    strParam("Runtime for the model decider (e.g. claude)"),
			"policy":             strParam("Instructions the decider follows: the user writes them, and a change from here is refused"),
			"on_decider_failure": strParam("deny | human — what full does when the decider fails"),
		}, []string{"org_name"}),
	}
}

// executeOrgUnification handles the tools above; ok is false for any other
// name.
func (mt *MonoagentTools) executeOrgUnification(ctx context.Context, name, args string) (string, bool, error) {
	switch name {
	case "add_org_automation":
		out, err := mt.addOrgAutomation(ctx, args)
		return out, true, err
	case "set_org_grant":
		out, err := mt.setOrgGrant(ctx, args)
		return out, true, err
	case "set_org_autonomy":
		out, err := mt.setOrgAutonomy(ctx, args)
		return out, true, err
	}
	return "", false, nil
}

// runOrgCLI runs `monoagentcli --profile <active> org <args…>` and returns
// its JSON line (the CLI may print warnings to stderr before it).
func (mt *MonoagentTools) runOrgCLI(ctx context.Context, tool string, args ...string) (string, error) {
	if mt.selfBin == "" {
		return "", fmt.Errorf("%s: unavailable in this session (no monoagentcli binary)", tool)
	}
	cctx, cancel, err := runExecTimeoutCtx(ctx)
	if err != nil {
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	defer cancel()
	full := append([]string{"--profile", mt.ProfileID(), "org"}, args...)
	out, err := runSelfExec(cctx, mt.selfBin, full...)
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", tool, err, truncateRunErrOutput(strings.TrimSpace(string(out))))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); json.Valid([]byte(l)) && strings.HasPrefix(l, "{") {
			return l, nil
		}
	}
	return "", fmt.Errorf("%s: unexpected output: %s", tool, truncateRunErrOutput(string(out)))
}

type addOrgAutomationArgs struct {
	OrgName    string `json:"org_name"`
	WorkflowID string `json:"workflow_id"`
	Alias      string `json:"alias"`
	Owned      bool   `json:"owned"`
}

func (mt *MonoagentTools) addOrgAutomation(ctx context.Context, args string) (string, error) {
	var a addOrgAutomationArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := mt.checkWorkflowOwnership(a.WorkflowID); err != nil {
		return "", err
	}
	cli := []string{"automation", "add", a.OrgName, "--workflow", a.WorkflowID, "--alias", a.Alias}
	if a.Owned {
		cli = append(cli, "--owned")
	}
	return mt.runOrgCLI(ctx, "add_org_automation", cli...)
}

type setOrgGrantArgs struct {
	OrgName        string `json:"org_name"`
	RoleID         string `json:"role_id"`
	Automation     string `json:"automation"`
	Mode           string `json:"mode"`
	Wait           *bool  `json:"wait"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Approval       string `json:"approval"`
	MaxCallsPerRun int    `json:"max_calls_per_run"`
	Revoke         bool   `json:"revoke"`
	Confirm        bool   `json:"confirm"`
}

func (mt *MonoagentTools) setOrgGrant(ctx context.Context, args string) (string, error) {
	var a setOrgGrantArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := mt.checkInjectionGate("set_org_grant"); err != nil {
		return "", err
	}
	if strings.EqualFold(strings.TrimSpace(a.Approval), "none") && !a.Revoke {
		// The chat is not the operator: a grant it makes always asks first
		// (`org grant add` refuses none from it, and defaults to required).
		return "", fmt.Errorf("set_org_grant: approval none lets the role run the automation without asking, which only the user can grant " +
			"(`monoagentcli org grant add ... --approval none` in their own terminal, or the app); omit approval for a grant whose calls ask first")
	}
	if !a.Revoke {
		// A grant is the user's to give: the chat's marker would make the CLI refuse it, so the tool stores a proposal.
		in := grantProposalJSON{Role: a.RoleID, Automation: a.Automation, Mode: a.Mode, Wait: a.Wait, TimeoutSeconds: a.TimeoutSeconds,
			Approval: strings.TrimSpace(a.Approval), MaxCallsPerRun: a.MaxCallsPerRun}
		return mt.proposeOrg(ctx, "set_org_grant", a.OrgName, orgproposal.VerbGrantAdd, in)
	}
	cli := []string{"grant", "remove", a.OrgName, "--role", a.RoleID, "--automation", a.Automation}
	if !a.Confirm {
		return marshalJSON(map[string]interface{}{
			"would_apply": true,
			"command":     orgCommandLine(cli),
			"note":        "this revokes the role's right to run the automation; call again with confirm:true to apply",
		})
	}
	return mt.runOrgCLI(ctx, "set_org_grant", cli...)
}

// grantProposalJSON is the arguments of a grant.add proposal, as `org proposal create --args-json` takes them.
type grantProposalJSON struct {
	Role           string `json:"role"`
	Automation     string `json:"automation"`
	Mode           string `json:"mode,omitempty"`
	Wait           *bool  `json:"wait,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Approval       string `json:"approval,omitempty"`
	MaxCallsPerRun int    `json:"max_calls_per_run,omitempty"`
}

// autonomyProposalJSON is the arguments of an autonomy.set proposal.
type autonomyProposalJSON struct {
	Level            string   `json:"level,omitempty"`
	Decider          string   `json:"decider,omitempty"`
	DeciderRuntime   string   `json:"decider_runtime,omitempty"`
	DeciderModel     string   `json:"decider_model,omitempty"`
	DeciderThreshold *float64 `json:"decider_threshold,omitempty"`
	OnDeciderFailure string   `json:"on_decider_failure,omitempty"`
}

type setOrgAutonomyArgs struct {
	OrgName          string   `json:"org_name"`
	Level            string   `json:"level"`
	Decider          string   `json:"decider"`
	DeciderModel     string   `json:"decider_model"`
	DeciderRuntime   string   `json:"decider_runtime"`
	DeciderThreshold *float64 `json:"decider_threshold"`
	Policy           *string  `json:"policy"`
	OnDeciderFailure string   `json:"on_decider_failure"`
}

func (mt *MonoagentTools) setOrgAutonomy(ctx context.Context, args string) (string, error) {
	var a setOrgAutonomyArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := mt.checkInjectionGate("set_org_autonomy"); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Level) == orgdesign.LevelFull {
		// The chat is not the operator: full puts no person in any decision
		// (`org autonomy set` refuses it from the chat too).
		return "", fmt.Errorf("set_org_autonomy: level full lets the org's decider handle everything with no person involved, which only the user can choose " +
			"(`monoagentcli org autonomy set <org> --level full` in their own terminal, or the app); use manual or mid")
	}
	if a.Policy != nil {
		// The policy is the decider's instructions, and which of two lets
		// more through can't be told: the user writes it.
		return "", fmt.Errorf("set_org_autonomy: the policy is the instructions the org's decider follows, which only the user writes " +
			"(`monoagentcli org autonomy set <org> --policy ...` in their own terminal, or the app); leave policy out")
	}
	if a.Decider != "" && !orgdesign.ValidDeciderKind(a.Decider) {
		return "", fmt.Errorf("decider %q must be one of %s", a.Decider, strings.Join(orgdesign.DeciderKinds, ", "))
	}
	in := autonomyProposalJSON{Level: strings.TrimSpace(a.Level), Decider: a.Decider, DeciderRuntime: a.DeciderRuntime,
		DeciderModel: a.DeciderModel, OnDeciderFailure: a.OnDeciderFailure}
	if t := a.DeciderThreshold; t != nil {
		if !(*t > 0 && *t <= 1) {
			return "", fmt.Errorf("decider_threshold %v must be in (0,1]", *t)
		}
		in.DeciderThreshold = t
	}
	// Autonomy is the user's to change: the chat's marker would make the CLI refuse it, so the tool stores a proposal.
	return mt.proposeOrg(ctx, "set_org_autonomy", a.OrgName, orgproposal.VerbAutonomySet, in)
}

// orgCommandLine is the operator's command for args, each one shell-quoted unless it is plainly safe:
// names an agent supplied stay one inert word when the user copies the line.
func orgCommandLine(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellWord(a)
	}
	return "monoagentcli org " + strings.Join(q, " ")
}

func shellWord(s string) string {
	safe := s != ""
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-.,:/=@%+", r)) {
			safe = false
			break
		}
	}
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
