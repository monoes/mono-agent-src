package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/orgdesign"
)

// Org × workflow unification tools (plan §7.1, §7.7). Each shells back into
// `monoagentcli org …` — the same code path the CLI and the GUI use — so
// grant rows, reconcile, and autonomy rules live in one place. Granting a
// role an automation or raising an org's autonomy hands agents new power,
// so both refuse once untrusted synced communications entered the session.
// Both are the user's to do (class B: `org grant add` and `org autonomy set` refuse
// the chat's marker), so they only PROPOSE: the tool runs nothing and says what the
// user would confirm in the org screen. Revoking a grant narrows access and still
// previews, then shells `org grant remove` on confirm:true.

func orgUnificationToolDefs(def func(name, desc string, props map[string]interface{}, required []string) ToolDef) []ToolDef {
	return []ToolDef{
		def("add_org_automation", "Add an existing workflow to an org as one of its automations, under a short alias (lowercase letters, digits, underscores). Roles can then be granted it as a tool with set_org_grant, or it can become an automation role.", map[string]interface{}{
			"org_name":    strParam("The org's name"),
			"workflow_id": strParam("The workflow to add (same profile)"),
			"alias":       strParam("Name the org uses for it; becomes the tool automation_<alias>"),
			"owned":       boolParam("Whether this org owns the workflow's lifecycle (at most one org may)"),
		}, []string{"org_name", "workflow_id", "alias"}),
		def("set_org_grant", "Grant (or with revoke:true, revoke) one org role the right to call one of the org's automations as a tool. Workflows with outbound nodes (email, chat, social, service writes) default to needing a decision per call. This only proposes the change: it applies nothing, and the user confirms it in the org screen. Revoking (revoke:true) previews, then applies with confirm:true.", map[string]interface{}{
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
		def("set_org_autonomy", "Change who decides an org's pending approvals, questions, and gates: level manual (a human decides everything), mid (rules approve routine items, the decider handles consequential ones, a human handles irreversible ones), or full (the decider handles everything, no human: only the user sets it, in their own terminal or the app). This only proposes the change: it applies nothing, and the user confirms it in the org screen.", map[string]interface{}{
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
	if strings.TrimSpace(a.Approval) == "none" && !a.Revoke {
		// The chat is not the operator: a grant it makes always asks first
		// (`org grant add` refuses none from it, and defaults to required).
		return "", fmt.Errorf("set_org_grant: approval none lets the role run the automation without asking, which only the user can grant " +
			"(`monoagentcli org grant add ... --approval none` in their own terminal, or the app); omit approval for a grant whose calls ask first")
	}
	var cli []string
	if a.Revoke {
		cli = []string{"grant", "remove", a.OrgName, "--role", a.RoleID, "--automation", a.Automation}
	} else {
		cli = []string{"grant", "add", a.OrgName, "--role", a.RoleID, "--automation", a.Automation}
		if a.Mode != "" {
			cli = append(cli, "--mode", a.Mode)
		}
		if a.Wait != nil {
			cli = append(cli, "--wait="+strconv.FormatBool(*a.Wait))
		}
		if a.TimeoutSeconds > 0 {
			cli = append(cli, "--timeout", strconv.Itoa(a.TimeoutSeconds))
		}
		if a.Approval != "" {
			cli = append(cli, "--approval", a.Approval)
		}
		if a.MaxCallsPerRun > 0 {
			cli = append(cli, "--max-calls-per-run", strconv.Itoa(a.MaxCallsPerRun))
		}
	}
	if !a.Revoke {
		// A grant is the user's to give: the chat's marker would make the CLI refuse it, so nothing runs.
		return orgProposal("grant role "+a.RoleID+" of org "+a.OrgName+" the automation "+a.Automation+grantDetail(a), cli)
	}
	if !a.Confirm {
		return marshalJSON(map[string]interface{}{
			"would_apply": true,
			"command":     orgCommandLine(cli),
			"note":        "this revokes the role's right to run the automation; call again with confirm:true to apply",
		})
	}
	return mt.runOrgCLI(ctx, "set_org_grant", cli...)
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
	cli := []string{"autonomy", "set", a.OrgName, "--by", "chat"}
	if a.Level != "" {
		cli = append(cli, "--level", a.Level)
	}
	if a.Decider != "" {
		cli = append(cli, "--decider", a.Decider)
	}
	if t := a.DeciderThreshold; t != nil {
		if !(*t > 0 && *t <= 1) {
			return "", fmt.Errorf("decider_threshold %v must be in (0,1]", *t)
		}
		cli = append(cli, "--decider-threshold", strconv.FormatFloat(*t, 'f', -1, 64))
	}
	if a.DeciderModel != "" {
		cli = append(cli, "--decider-model", a.DeciderModel)
	}
	if a.DeciderRuntime != "" {
		cli = append(cli, "--decider-runtime", a.DeciderRuntime)
	}
	if a.Policy != nil {
		cli = append(cli, "--policy", *a.Policy)
	}
	if a.OnDeciderFailure != "" {
		cli = append(cli, "--on-decider-failure", a.OnDeciderFailure)
	}
	// Autonomy is the user's to change: the chat's marker would make the CLI refuse it, so nothing runs.
	return orgProposal("change org "+a.OrgName+"'s autonomy"+autonomyDetail(a), cli)
}

// orgProposal is what a tool answers for a change only the user makes: nothing was applied, and the
// note says what the user would confirm in the org screen. command is the operator's equivalent.
func orgProposal(change string, cli []string) (string, error) {
	return marshalJSON(map[string]interface{}{
		"applied":  false,
		"proposal": change,
		"command":  orgCommandLine(cli),
		"note":     "proposal: confirm in the org screen. Nothing was changed; this would " + change + ".",
	})
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

func grantDetail(a setOrgGrantArgs) string {
	var d []string
	if a.Mode != "" {
		d = append(d, "mode "+a.Mode)
	}
	if a.Wait != nil {
		d = append(d, "wait "+strconv.FormatBool(*a.Wait))
	}
	if a.TimeoutSeconds > 0 {
		d = append(d, "timeout "+strconv.Itoa(a.TimeoutSeconds)+"s")
	}
	if a.Approval != "" {
		d = append(d, "approval "+a.Approval)
	}
	if a.MaxCallsPerRun > 0 {
		d = append(d, "max calls per run "+strconv.Itoa(a.MaxCallsPerRun))
	}
	if len(d) == 0 {
		return ""
	}
	return " (" + strings.Join(d, ", ") + ")"
}

func autonomyDetail(a setOrgAutonomyArgs) string {
	var d []string
	if a.Level != "" {
		d = append(d, "level "+a.Level)
	}
	if a.Decider != "" {
		d = append(d, "decider "+a.Decider)
	}
	if a.DeciderThreshold != nil {
		d = append(d, "decider threshold "+strconv.FormatFloat(*a.DeciderThreshold, 'f', -1, 64))
	}
	if a.DeciderModel != "" {
		d = append(d, "decider model "+a.DeciderModel)
	}
	if a.DeciderRuntime != "" {
		d = append(d, "decider runtime "+a.DeciderRuntime)
	}
	if a.OnDeciderFailure != "" {
		d = append(d, "on decider failure "+a.OnDeciderFailure)
	}
	if len(d) == 0 {
		return ""
	}
	return " to: " + strings.Join(d, ", ")
}
