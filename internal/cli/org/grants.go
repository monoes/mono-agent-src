package org

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ─── shared helpers ──────────────────────────────────────────────────────

func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// docCarriesEnforcedKeys reports whether saving d needs the grant store:
// anything reconcile would have to back with a row.
func docCarriesEnforcedKeys(d *orgdesign.Doc) bool {
	for _, r := range d.Roles {
		if len(r.Automations) > 0 || len(r.ToolProviders) > 0 || r.IsEndpoint() {
			return true
		}
	}
	return false
}

// orgWorkflowIn loads a workflow and checks it belongs to profileID.
// Workflows without a profile (legacy, file-store) count as the default
// profile's.
func (env *orgEnv) orgWorkflowIn(ctx context.Context, db *storage.Database, profileID, id string) (*workflow.Workflow, error) {
	wf, err := env.OpenWorkflowStore(db).GetWorkflow(ctx, id)
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return nil, fmt.Errorf("workflow %q not found", id)
	}
	owner := wf.ProfileID
	if owner == "" {
		return nil, fmt.Errorf("workflow %q has no profile, so it cannot be granted", id)
	}
	if owner != profileID {
		return nil, fmt.Errorf("workflow %q belongs to profile %q, not %q (v1 grants stay inside one profile)", id, owner, profileID)
	}
	return wf, nil
}

// loadOrgForRowEdit is loadOrgForEdit for a command that writes a row and then the file: it takes
// the org rows lock first, so the doc it loads is the one the daemon has finished reconciling, and
// holds the lock until release.
func loadOrgForRowEdit(env *orgEnv, name string) (db *storage.Database, profileID, root string, doc *orgdesign.Doc, release func(), err error) {
	if _, _, root, err = env.Profile(); err != nil {
		return nil, "", "", nil, nil, err
	}
	if release, err = orgenv.LockRows(root); err != nil {
		return nil, "", "", nil, nil, err
	}
	if db, profileID, root, doc, err = loadOrgForEdit(env, name); err != nil {
		release()
		return nil, "", "", nil, nil, err
	}
	return db, profileID, root, doc, release, nil
}

func loadOrgForEdit(env *orgEnv, name string) (*storage.Database, string, string, *orgdesign.Doc, error) {
	db, profileID, root, err := env.Profile()
	if err != nil {
		return nil, "", "", nil, err
	}
	doc, err := orgdesign.Load(root, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", "", nil, fmt.Errorf("org %q not found under %s", name, orgdesign.OrgsDir(root))
		}
		return nil, "", "", nil, err
	}
	return db, profileID, root, doc, nil
}

// grantCapabilityWarnings explains, without failing the command, when the
// installed monomind cannot yet deliver what was just configured.
func capabilityWarnings(ctx context.Context, cap, feature string) []string {
	set, err := monomind.Capabilities(ctx)
	if err != nil {
		return []string{fmt.Sprintf("%s: monomind unavailable (%v)", feature, err)}
	}
	if err := set.Require(cap, feature); err != nil {
		return []string{err.Error()}
	}
	return nil
}

// ─── org grant ───────────────────────────────────────────────────────────

func newOrgGrantCmd(env *orgEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "grant",
		Short: "Let org roles call automations as tools",
	}
	cmd.AddCommand(newOrgGrantListCmd(env), newOrgGrantAddCmd(env), newOrgGrantRemoveCmd(env))
	return cmd
}

type grantView struct {
	ID              string `json:"id"`
	Org             string `json:"org"`
	Role            string `json:"role"`
	Alias           string `json:"alias"`
	WorkflowID      string `json:"workflow_id"`
	WorkflowName    string `json:"workflow_name"`
	Mode            string `json:"mode"`
	Wait            bool   `json:"wait"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	Approval        string `json:"approval"`
	Tier            string `json:"tier"`
	MaxCallsPerRun  int    `json:"max_calls_per_run"`
	MaxCallsPerDay  int    `json:"max_calls_per_day"`
	MaxOutputBytes  int    `json:"max_output_bytes"`
	CreatedAt       string `json:"created_at"`
	WorkflowMissing bool   `json:"workflow_missing,omitempty"`
	// FileInputNodes: see automationView (C-46).
	FileInputNodes []orggrant.FileInputNode `json:"file_input_nodes"`
}

func (env *orgEnv) viewGrant(ctx context.Context, db *storage.Database, g orggrant.Grant) grantView {
	t := g.Automation()
	v := grantView{
		ID: g.ID, Org: g.OrgName, Role: g.RoleID, Alias: t.Alias, WorkflowID: t.WorkflowID,
		Mode: t.Mode, Wait: t.Wait, TimeoutSeconds: t.Timeout, Approval: t.Approval, Tier: t.Tier,
		MaxCallsPerRun: t.MaxCallsPerRun, MaxCallsPerDay: t.MaxCallsPerDay, MaxOutputBytes: t.MaxOutputBytes,
		CreatedAt:      g.CreatedAt.UTC().Format(time.RFC3339),
		FileInputNodes: []orggrant.FileInputNode{},
	}
	// The tier follows the workflow as it is now, not as it was at grant
	// time: adding an email node to a granted workflow raises its calls to
	// irreversible immediately.
	if wf, err := env.orgWorkflowIn(ctx, db, g.ProfileID, t.WorkflowID); err == nil {
		v.WorkflowName = wf.Name
		v.Tier = orggrant.GrantTier(orggrant.OutboundNodes(wf))
		if fi := orggrant.FileInputNodes(wf); len(fi) > 0 {
			v.FileInputNodes = fi
		}
	} else {
		v.WorkflowMissing = true
	}
	return v
}

func newOrgGrantListCmd(env *orgEnv) *cobra.Command {
	var role string
	c := &cobra.Command{
		Use:   "list <org>",
		Short: "List automation grants",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, profileID, _, doc, err := loadOrgForEdit(env, args[0])
			if err != nil {
				return err
			}
			grants, err := orggrant.NewStore(db.DB).ListGrants(cmd.Context(), profileID, doc.Name, role)
			if err != nil {
				return err
			}
			out := []grantView{}
			for _, g := range grants {
				if g.Automation() != nil {
					out = append(out, env.viewGrant(cmd.Context(), db, g))
				}
			}
			return printJSONValue(map[string]interface{}{"v": 1, "org": doc.Name, "grants": out})
		},
	}
	c.Flags().StringVar(&role, "role", "", "Only this role's grants")
	return c
}

func newOrgGrantAddCmd(env *orgEnv) *cobra.Command {
	var role, alias, mode, approval string
	var wait bool
	var timeout, maxRun, maxDay, maxOut int
	c := &cobra.Command{
		Use:   "add <org>",
		Short: "Grant a role an automation as a tool (updates the grant when it exists)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// What is not the operator's own terminal (an agent, the chat, a workflow, a process inside a
			// coder turn: opgate) gets no grant at all (class B), and learns nothing from a lookup first.
			if why := opgate.Refusal(); why != "" {
				if cmd.Flags().Changed("approval") && approval == "none" {
					return env.d.InvalidInput("a grant with --approval none lets role %q run %q without asking, which is the user's to give: %s; "+
						"leave --approval out (or pass required), or run it yourself from a terminal or the app", role, alias, why)
				}
				return env.refuseNotOperator(fmt.Sprintf("granting %q to role %q", alias, role))
			}
			db, profileID, root, doc, release, err := loadOrgForRowEdit(env, args[0])
			if err != nil {
				return err
			}
			defer release()
			r, _ := doc.FindRole(role)
			if r == nil {
				return fmt.Errorf("org %q has no role %q", doc.Name, role)
			}
			if r.IsEndpoint() {
				return fmt.Errorf("role %q is an automation role; only agent roles hold grants", role)
			}
			ref := doc.FindAutomation(alias)
			if ref == nil {
				return fmt.Errorf("org %q has no automation %q; add it first with `org automation add`", doc.Name, alias)
			}
			switch mode {
			case "run", "trigger", "status":
			default:
				return fmt.Errorf("--mode must be run, trigger, or status")
			}
			wf, err := env.orgWorkflowIn(ctx, db, profileID, ref.WorkflowID)
			if err != nil {
				return err
			}
			outbound := orggrant.OutboundNodes(wf)
			if !cmd.Flags().Changed("approval") {
				// C-34: side effects outside mono-agent need a decision by default.
				approval = "none"
				if len(outbound) > 0 {
					approval = "required"
				}
			}
			if approval != "none" && approval != "required" {
				return fmt.Errorf("--approval must be none or required")
			}
			store := orggrant.NewStore(db.DB)
			existing, _ := store.ListGrants(ctx, profileID, doc.Name, role)

			tool := orggrant.Tool{
				Alias: alias, WorkflowID: ref.WorkflowID, Mode: mode, Wait: wait, Timeout: timeout,
				Approval: approval, Tier: orggrant.GrantTier(outbound),
				MaxCallsPerRun: maxRun, MaxCallsPerDay: maxDay, MaxOutputBytes: maxOut,
			}
			// Display copy first, so reconcile finds the JSON counterpart of
			// the row it is about to see.
			if spec := r.FindGrantSpec(alias); spec == nil {
				r.Automations = append(r.Automations, orgdesign.GrantSpec{Alias: alias})
			}
			g, err := store.UpsertGrant(ctx, orggrant.GrantInput{ProfileID: profileID, OrgName: doc.Name, RoleID: role, Tool: tool})
			if err != nil {
				return err
			}
			if len(existing) == 0 {
				// Q2: a role's first grant pre-fills denyTools Bash, because
				// Bash can run monoagentcli and bypass every grant (C-2). The
				// operator may remove it; later grants do not re-add it.
				deny := r.PolicyStrings("denyTools")
				if !containsString(deny, "Bash") {
					r.SetPolicyStrings("denyTools", append(deny, "Bash"))
				}
			}
			rep, err := orgenv.SaveReconciled(ctx, db, profileID, root, doc, env.genOptions(profileID))
			if err != nil {
				return err
			}
			var warnings []string
			warnings = append(warnings, capabilityWarnings(ctx, monomind.CapOrgToolProviders, "role automation tools")...)
			if !containsString(r.PolicyStrings("denyTools"), "Bash") {
				warnings = append(warnings, fmt.Sprintf("role %q can use Bash, which can run monoagentcli directly and bypass its grants", role))
			}
			if w := orggrant.FileInputWarning(orggrant.FileInputNodes(wf)); w != "" {
				warnings = append(warnings, w) // C-46
			}
			return printJSONValue(map[string]interface{}{
				"v": 1, "org": doc.Name, "grant": env.viewGrant(ctx, db, *g),
				"tool_name": orgdesign.ProviderName + "__automation_" + alias,
				"applies":   "next org start",
				"reconcile": rep.Findings,
				"warnings":  nonNilStrings(warnings),
			})
		},
	}
	c.Flags().StringVar(&role, "role", "", "Role id")
	c.Flags().StringVar(&alias, "automation", "", "Automation alias")
	c.Flags().StringVar(&mode, "mode", "run", "run | trigger | status")
	c.Flags().BoolVar(&wait, "wait", true, "Wait for the run to finish and return its output")
	c.Flags().IntVar(&timeout, "timeout", orggrant.DefaultTimeoutSeconds, "Seconds to wait when --wait")
	c.Flags().StringVar(&approval, "approval", "none", "none | required (default: required when the workflow has outbound nodes, or when an agent, the chat or a workflow asks; none is operator only)")
	c.Flags().IntVar(&maxRun, "max-calls-per-run", orggrant.DefaultMaxCallsPerRun, "Calls allowed per org run")
	c.Flags().IntVar(&maxDay, "max-calls-per-day", orggrant.DefaultMaxCallsPerDay, "Calls allowed per day")
	c.Flags().IntVar(&maxOut, "max-output-bytes", orggrant.DefaultMaxOutputBytes, "Output bytes returned to the role per call")
	_ = c.MarkFlagRequired("role")
	_ = c.MarkFlagRequired("automation")
	return c
}

func newOrgGrantRemoveCmd(env *orgEnv) *cobra.Command {
	var role, alias string
	c := &cobra.Command{
		Use:   "remove <org>",
		Short: "Revoke a role's grant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			db, profileID, root, doc, release, err := loadOrgForRowEdit(env, args[0])
			if err != nil {
				return err
			}
			defer release()
			store := orggrant.NewStore(db.DB)
			if err := store.RevokeGrant(ctx, profileID, doc.Name, role, alias); err != nil {
				if errors.Is(err, orggrant.ErrNotFound) {
					return fmt.Errorf("role %q of org %q has no grant for %q", role, doc.Name, alias)
				}
				return err
			}
			if _, err := orgenv.SaveReconciled(ctx, db, profileID, root, doc, env.genOptions(profileID)); err != nil {
				return err
			}
			return printJSONValue(map[string]interface{}{"v": 1, "org": doc.Name, "removed": true})
		},
	}
	c.Flags().StringVar(&role, "role", "", "Role id")
	c.Flags().StringVar(&alias, "automation", "", "Automation alias")
	_ = c.MarkFlagRequired("role")
	_ = c.MarkFlagRequired("automation")
	return c
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
