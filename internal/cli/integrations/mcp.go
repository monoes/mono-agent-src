package integrations

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/mcp"
)

// NewMCPCommand returns the `mcp` cobra command: a stdio JSON-RPC MCP server
// exposing workflows, nodes, the HIL queue, monoagent-domain tools (vault,
// secrets, people, orgs — the same surface the chat feature uses), and
// reference docs as tools for AI agents. stdout is the protocol channel;
// logs go to stderr only.
func (d Deps) NewMCPCommand() *cobra.Command {
	var allowMutations, allowAPIExposure, apiOnly, tasksOnly bool
	var grant, workflowTools string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run MCP server (stdio) for AI agents",
		Long: `Run a Model Context Protocol server over stdin/stdout
(newline-delimited JSON-RPC 2.0).

Register this command as a stdio MCP server in any MCP client. One server
serves every profile: it holds no profile, and every tool that acts on a profile
takes a required "profile" argument (the profile's id or name) on each call. A
call without it, or naming an unknown or ambiguous profile, is refused with the
list of profiles. Only node_list, node_schema, docs, workflow_template_list,
workflow_template_show, template_list, api_config_get, api_config_set and
api_config_apply act on the machine and take no profile. The exception is
--grant, which serves the one profile of its grant and needs --profile.

Read-only tools are always exposed: workflow_list, workflow_get, workflow_validate,
workflow_status, node_list, node_schema, hil_list, vault_item_list,
vault_item_get_path, profile_document_search, secret_list, person_list,
person_get, message_list, message_get, social_list_list, template_list,
org_list, org_get, org_validate, api_key_list, api_models_list, api_status,
api_config_get, task_list, task_get, task_next, task_session_list, task_session_get,
session_list, session_status (a coder turn's session token only), docs.

Mutating tools (workflow_run, workflow_create/delete/set_active/node_add,
hil_approve, hil_reject, secret_add/update/delete, person_upsert/delete,
org_create and every org_role_*/org_reload tool, api_key_create/update/revoke,
which change none of the OpenAI-compatible API's keys (below), api_config_set and
api_config_apply, which save its server's settings and restart the daemon that
reads them, api_auto_set, which switches its auto model on or off, and
task_claim/comment/finish/release/add/split/verify/artifact, which work the user's task board, and
session_spawn/send/pause/resume/stop/set, which a master coder session uses on its child sessions and which
refuse unless the process holds the session token of a coder turn) are only
exposed with --allow-mutations or MONOAGENT_MCP_ALLOW_MUTATIONS=1 — without it they are
omitted from tools/list and refuse with an explanatory error if called by
name. This includes workflow_run/hil_approve/hil_reject, which were exposed
unconditionally before this flag existed — add --allow-mutations to an
existing MCP client config that relies on them.

api_key_create, api_key_update and api_key_revoke change no key, for any caller:
only you create, change or revoke an API key, with monoagentcli api key
create|update|revoke in your own terminal (they refuse an agent's shell tool) or
in the app, so a new key is in no transcript.

api_config_set saves settings of the OpenAI-compatible API's server and refuses a
change that makes the server reach further than it did, and saves nothing, unless
this server was started with --allow-api-exposure or MONOAGENT_MCP_ALLOW_API_EXPOSURE=1.
What reaches further is: a dedicated listener that reaches further than the saved
one (beyond this machine, another host beyond it, or every interface where it was
one host), a higher confinement class, a runtime list that gains a runtime it did
not have, none left (tool calling or image generation switched on again), and
removing a saved row that cannot be read (saved_settings). That is the operator's
decision, made here: the arguments of a tool are set by the model, so none of them
can lift the refusal. It adds nothing without --allow-mutations, and it guards that
one tool and api_auto_set only: --allow-mutations also serves workflow_node_add
(which accepts the node type system.execute_command), workflow_set_active and
workflow_run. A workflow a model writes runs no program until you approve it, and
"monoagentcli api config set" refuses a workflow's process anyway (exit 3,
operator_only), but an approved workflow's own steps run as you. If a model must
not be able to start your workflows, do not give it --allow-mutations, or start
the server with --api-only.

--api-only (or MONOAGENT_MCP_API_ONLY=1) serves the API's tools (api_*) and no
other: no workflow, vault, secret, person, org or documentation tool. It is for a
model that is to manage the API and nothing else: --allow-mutations, which the
API's mutating tools need, also serves the workflow tools above, and with
--api-only it does not. It takes tools away and changes none that stay: the
mutating ones still need --allow-mutations, and --allow-api-exposure is still
what lets api_config_set widen the server and api_auto_set switch the auto model
on. A host that has tools of its own, such as a shell tool, is not stopped by it.

--tasks-only (or MONOAGENT_MCP_TASKS_ONLY=1) serves the user's task board's tools
(task_*) and no other: no workflow, vault, secret, person, org, API or
documentation tool. It is for an agent that is to work the board and nothing
else: --allow-mutations, which task_claim, task_comment, task_finish,
task_release, task_add, task_split, task_verify and task_artifact need, also serves workflow tools that can run a
command as you, and with --tasks-only it does not. The task tools act on the
profile each call names, as the agent agent:<client>#<4 hex digits>, named after
the MCP client and the session; no tool approves, edits, moves or archives a task.
A host that has tools of its own, such as a shell tool, is not stopped by it.
Register one server for all profiles; the agent passes "profile" on each call:

  claude mcp add monoagent-tasks -- monoagentcli mcp --tasks-only --allow-mutations

It cannot be combined with --api-only or --grant.

--workflow-tools (or MONOAGENT_MCP_WORKFLOW_TOOLS=1) also serves each active
workflow of every profile as a tool of its own, wf_<slug>, named after the workflow
(lowercase a-z, 0-9 and _, at most 64 characters; a second workflow with the same
name gets a short id suffix). --workflow-tools=<id or name>,... serves only those.
A tool's arguments are the workflow's trigger input, the fields "workflow inputs"
lists, plus _timeout_seconds and the required "profile" (not part of the trigger
input): a workflow tool belongs to its workflow's own profile, and a call that
names another profile does not find the workflow. Calling it runs the workflow and waits, as
workflow_run does, with the same result. They are mutating tools, so they need
--allow-mutations too. Workflows are read at every tools/list, so one saved later
is served without a restart. It cannot be combined with --api-only, --tasks-only
or --grant.

api_config_apply restarts the daemon, as daemon restart does, and interrupts what
it is running (workflows, org runs). api_auto_set switches the auto model on only
on a server started with --allow-api-exposure too (what leaves the machine is the
operator's decision: prompts then go to TypeSafe) and with acknowledge_egress:
true; switching it off needs neither. It never creates or reads the Jev key.

Honors --db-path. Neither MONOAGENT_PROFILE nor an active profile is read: --profile is
needed only with --grant.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A process monomind spawned for an org role may only serve a
			// grant: a hand-added provider running plain `mcp` would give
			// the role every mutating tool (C-3).
			if grant == "" && os.Getenv("MONOMIND_ORG_NAME") != "" {
				return fmt.Errorf("refusing to serve the full MCP tool set to an org role (MONOMIND_ORG_NAME is set); org roles use `mcp --grant <id>`")
			}
			if grant != "" && allowMutations {
				return fmt.Errorf("--grant serves only the granted automations; --allow-mutations does not apply")
			}
			if grant != "" && allowAPIExposure {
				return fmt.Errorf("--grant serves only the granted automations; --allow-api-exposure does not apply")
			}
			if grant != "" && apiOnly {
				return fmt.Errorf("--grant serves only the granted automations; --api-only does not apply")
			}
			if grant != "" && tasksOnly {
				return fmt.Errorf("--grant serves only the granted automations; --tasks-only does not apply")
			}
			if grant != "" && workflowTools != "" {
				return fmt.Errorf("--grant serves only the granted automations; --workflow-tools does not apply")
			}
			if workflowTools != "" && (apiOnly || tasksOnly) {
				return mcp.ErrWorkflowToolsWithNarrowMode
			}
			if tasksOnly && apiOnly {
				return mcp.ErrTasksOnlyWithAPIOnly
			}
			// The server holds no profile; only a grant server is given one, to check
			// it against its grant.
			grantProfile := ""
			if grant != "" {
				profileFlag := cmd.Flag("profile")
				if profileFlag == nil || !profileFlag.Changed || profileFlag.Value.String() == "" {
					return fmt.Errorf("--grant needs --profile <id|name>: the profile the grant belongs to (grant mode serves exactly that profile)")
				}
				grantProfile = profileFlag.Value.String()
			}
			return d.RunMCP(mcp.Options{
				DBPath:           d.DBPath(),
				Profile:          grantProfile,
				Version:          d.Version,
				AllowMutations:   allowMutations,
				AllowAPIExposure: allowAPIExposure,
				APIOnly:          apiOnly,
				TasksOnly:        tasksOnly,
				Grant:            grant,
				WorkflowTools:    workflowTools,
			})
		},
	}
	cmd.Flags().StringVar(&grant, "grant", "",
		"Grant mode: serve only the automations granted to one org role (monomind spawns this for role tool providers)")
	cmd.Flags().BoolVar(&allowMutations, "allow-mutations", false,
		"Serve mutating tools (workflow_run, hil_approve/reject, and create/update/delete-class workflow/secret/person/org/api-key tools, and api_config_set, api_config_apply and api_auto_set, and the task board's task_claim/comment/finish/release/add); also settable via MONOAGENT_MCP_ALLOW_MUTATIONS=1")
	cmd.Flags().BoolVar(&allowAPIExposure, "allow-api-exposure", false,
		"Let api_config_set save a change that makes the OpenAI-compatible API's server reach further, which it otherwise refuses: a dedicated listener that reaches further than the saved one (beyond this machine, another host beyond it, or every interface where it was one host), a higher confinement class, a runtime list that gains a runtime it did not have, none left (tool calling or image generation switched on again), and removing a saved row that cannot be read (saved_settings); needs --allow-mutations; it guards that tool only (--allow-mutations also serves workflow tools that can run a command as you); also settable via MONOAGENT_MCP_ALLOW_API_EXPOSURE=1")
	cmd.Flags().BoolVar(&apiOnly, "api-only", false,
		"Serve only the OpenAI-compatible API's tools (api_*) and no other: no workflow, vault, secret, person, org or documentation tool. For a model that is to manage the API and nothing else: --allow-mutations, which the API's mutating tools need, also serves workflow tools that can run a command as you; with --api-only it does not. The mutating API tools still need --allow-mutations, and --allow-api-exposure is still what lets api_config_set widen the server and api_auto_set switch the auto model on; also settable via MONOAGENT_MCP_API_ONLY=1")
	cmd.Flags().BoolVar(&tasksOnly, "tasks-only", false,
		"Serve only the user's task board's tools (task_*) and no other: no workflow, vault, secret, person, org, API or documentation tool. For an agent that is to work the board and nothing else: --allow-mutations, which task_claim/comment/finish/release/add need, also serves workflow tools that can run a command as you; with --tasks-only it does not. Cannot be combined with --api-only or --grant; also settable via MONOAGENT_MCP_TASKS_ONLY=1")
	cmd.Flags().StringVar(&workflowTools, "workflow-tools", "",
		"Also serve saved workflows as tools wf_<slug> whose arguments are the workflow's trigger input and whose call runs it and waits, as workflow_run does: --workflow-tools for every active workflow of the profile, --workflow-tools=<id or name>,... for those only (inactive workflows are not served: they cannot run). Mutating: needs --allow-mutations too. Cannot be combined with --api-only, --tasks-only or --grant; also settable via MONOAGENT_MCP_WORKFLOW_TOOLS=1 (or the list)")
	cmd.Flags().Lookup("workflow-tools").NoOptDefVal = "all"
	return d.Serving(cmd)
}
