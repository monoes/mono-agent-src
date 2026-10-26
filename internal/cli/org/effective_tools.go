package org

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
)

// ─── org effective-tools ─────────────────────────────────────────────────

// orgToolNames are the tools monomind's buildOrgTools gives every agent
// role (session.ts), and bossOrgTools the ones only the boss gets. Kept here
// as a display list; monomind remains the authority.
var orgToolNames = []string{
	"org_send", "ask_human", "org_gate", "org_task", "org_task_done", "org_tasks",
	"org_task_split", "org_task_merge", "org_task_cancel", "org_task_block", "org_plan_graph",
	"org_recall", "org_remember", "org_learn",
}
var bossOrgTools = []string{"org_complete", "org_respawn_role", "org_list_runtime_options", "org_compact_role", "org_clear_role"}

// builtinRuntimeTools are the runtime's own tools a role has unless policy
// denies them.
var builtinRuntimeTools = []string{"Read", "Write", "Edit", "Glob", "Grep", "Bash", "WebFetch", "WebSearch"}

func newOrgEffectiveToolsCmd(env *orgEnv) *cobra.Command {
	var role string
	c := &cobra.Command{
		Use:   "effective-tools <org>",
		Short: "List the tools a role's model sees: org tools, granted automations, and runtime tools",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, profileID, _, doc, err := loadOrgForEdit(env, args[0])
			if err != nil {
				return err
			}
			r, _ := doc.FindRole(role)
			if r == nil {
				return fmt.Errorf("org %q has no role %q", doc.Name, role)
			}
			type tool struct {
				Name        string `json:"name"`
				Source      string `json:"source"`
				Description string `json:"description"`
				// C-46: granted automations whose file nodes take paths
				// from their input.
				FileInputNodes []orggrant.FileInputNode `json:"file_input_nodes,omitempty"`
			}
			tools := []tool{}
			if r.IsEndpoint() {
				return printJSONValue(map[string]interface{}{"v": 1, "org": doc.Name, "role": role, "tools": tools})
			}
			deny := map[string]bool{}
			for _, d := range r.PolicyStrings("denyTools") {
				deny[d] = true
			}
			allow := r.PolicyStrings("allowTools")
			for _, n := range orgToolNames {
				tools = append(tools, tool{Name: n, Source: "org", Description: "monomind org tool"})
			}
			if r.ReportsTo == nil {
				for _, n := range bossOrgTools {
					tools = append(tools, tool{Name: n, Source: "org", Description: "monomind org tool (boss only)"})
				}
			}
			grants, err := orggrant.NewStore(db.DB).ListGrants(cmd.Context(), profileID, doc.Name, role)
			if err != nil {
				return err
			}
			for _, name := range orggrant.ProviderToolNames(grants) {
				desc := "granted automation tool"
				var fileInputs []orggrant.FileInputNode
				for _, g := range grants {
					if t := g.Automation(); t != nil && t.Tool == name {
						desc = fmt.Sprintf("runs automation %q (%s, approval %s)", t.Alias, t.Mode, t.Approval)
						if wf, err := env.orgWorkflowIn(cmd.Context(), db, profileID, t.WorkflowID); err == nil {
							fileInputs = orggrant.FileInputNodes(wf)
						}
					}
				}
				if deny[orgdesign.ProviderName+"__"+name] {
					continue
				}
				tools = append(tools, tool{Name: orgdesign.ProviderName + "__" + name, Source: "grant", Description: desc, FileInputNodes: fileInputs})
			}
			for _, n := range builtinRuntimeTools {
				if deny[n] || (len(allow) > 0 && !containsString(allow, n)) {
					continue
				}
				tools = append(tools, tool{Name: n, Source: "builtin", Description: "runtime tool"})
			}
			return printJSONValue(map[string]interface{}{"v": 1, "org": doc.Name, "role": role, "tools": tools})
		},
	}
	c.Flags().StringVar(&role, "role", "", "Role id")
	_ = c.MarkFlagRequired("role")
	return c
}
