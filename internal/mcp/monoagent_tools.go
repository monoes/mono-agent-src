package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	aichat "github.com/monoes/mono-agent/internal/ai/chat"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// chatToolHandler adapts one internal/ai/chat MonoagentTools tool (chatName,
// dispatched through chat's own <verb>_<resource>-named ExecuteContext
// switch) into an MCP toolHandler under this package's <resource>_<verb>
// naming convention. One shared adapter backs every monoagentAdaptedTools()
// entry below, instead of one bespoke handler per tool.
func chatToolHandler(chatName string) toolHandler {
	return func(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
		rt, err := s.runtime()
		if err != nil {
			return nil, err
		}
		argsStr := "{}"
		if len(args) > 0 {
			argsStr = string(args)
		}
		mt := rt.chatTools(profileOf(ctx))
		result, err := mt.ExecuteContext(ctx, chatName, argsStr)
		rt.shareUntrusted(mt)
		if err != nil {
			return nil, err
		}
		// message_list/message_get wrap their result in a plain-text
		// provenance fence (marshalFenced — not JSON as a whole), and any
		// tool's result can be byte-truncated past JSON validity by
		// marshalJSON's size cap. Only promote to json.RawMessage (rendered
		// verbatim/pretty by callTool's MarshalIndent) when the string is
		// actually valid JSON; otherwise return the plain Go string so it
		// gets quoted/escaped like any other string result instead of
		// failing to render.
		if json.Valid([]byte(result)) {
			return json.RawMessage(result), nil
		}
		return result, nil
	}
}

func boolParam(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": desc}
}

func intParam(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "integer", "description": desc}
}

// monoagentAdaptedTools returns the MCP tool entries backed by chat's
// MonoagentTools (internal/ai/chat/monoagent_tools.go), renamed to this
// package's <resource>_<verb> convention. chat's own run_workflow,
// list_workflows, get_workflow, and list_node_types are deliberately not
// adapted here — see the plan's Dedup section: each is superseded by an
// existing native MCP tool.
func monoagentAdaptedTools() []tool {
	tools := []tool{
		{
			name:        "message_list",
			description: "List message/interaction history, optionally for one person. Results carry an untrusted-content provenance fence.",
			schema: objSchema(map[string]interface{}{
				"person_id": strParam("Optional: limit to one person"),
				"limit":     intParam("Max results (default 50)"),
			}),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("list_messages"),
		},
		{
			name:        "message_get",
			description: "Get a single message's full body. Result carries an untrusted-content provenance fence.",
			schema: objSchema(map[string]interface{}{
				"message_id": strParam("Message ID"),
			}, "message_id"),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("get_message"),
		},

		// Workflow authoring (workflow_list/get/run stay on the native,
		// engine-aware tools in tools.go — see the plan's Dedup section).
		{
			name:        "workflow_create",
			description: "Create a new, empty workflow to add nodes to with workflow_node_add, then run with the native workflow_run tool.",
			schema: objSchema(map[string]interface{}{
				"name":        strParam("Workflow name"),
				"description": strParam("Optional description"),
			}, "name"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("create_workflow"),
		},
		{
			name:        "workflow_delete",
			description: "Permanently delete a workflow and its nodes/connections/executions. Writes a pre-delete backup to ~/.monoagent/ai-tool-backups (fails closed if the backup can't be written).",
			schema: objSchema(map[string]interface{}{
				"workflow_id": strParam("The workflow ID"),
			}, "workflow_id"),
			annotations: map[string]bool{"destructiveHint": true},
			mutating:    true,
			handler:     chatToolHandler("delete_workflow"),
		},
		{
			name:        "workflow_set_active",
			description: "Activate or deactivate a workflow (active workflows run on their triggers).",
			schema: objSchema(map[string]interface{}{
				"workflow_id": strParam("The workflow ID"),
				"active":      boolParam("true to activate, false to deactivate"),
			}, "workflow_id", "active"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("set_workflow_active"),
		},
		{
			name:        "workflow_node_add",
			description: "Add a node to a workflow. node_type is \"<category>.<action>\" — use the native node_list tool if unsure of the exact string. config is a JSON object of that node type's settings.",
			schema: objSchema(map[string]interface{}{
				"workflow_id": strParam("The workflow ID to add the node to"),
				"node_type":   strParam("The node type, e.g. http.request"),
				"name":        strParam("A human-readable name for this node"),
				"config":      map[string]interface{}{"type": "object", "description": "Node configuration matching the node type's schema"},
			}, "workflow_id", "node_type", "name"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("add_workflow_node"),
		},

		// Vault (media: images, audio, video, and other files) and profile documents.
		{
			name:        "vault_item_list",
			description: "List the media vault: images, audio, video and other files stored in it (each has a kind: image, audio or video).",
			schema: objSchema(map[string]interface{}{
				"limit": intParam("Max items to return (default 50)"),
			}),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("list_vault_items"),
		},
		{
			name:        "vault_item_get_path",
			description: "Resolve a vault item id to its filesystem path.",
			schema: objSchema(map[string]interface{}{
				"vault_id": strParam("Vault item id, e.g. img-001"),
			}, "vault_id"),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("get_vault_item_path"),
		},
		{
			name:        "profile_document_search",
			description: "Search the profile's second brain (uploaded documents, captured pages) and get ranked passages with section paths. Use this first for questions about the user's documents or saved pages. Requires the monomind CLI on PATH — fails with an install hint if it isn't.",
			schema: objSchema(map[string]interface{}{
				"query": strParam("Search query"),
			}, "query"),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("search_profile_documents"),
		},

		// Credentials vault — metadata/reference only; values are never
		// returned by any tool.
		{
			name:        "secret_list",
			description: "List credential entries by name and metadata only. Values are never returned by any tool.",
			schema:      objSchema(nil),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("list_secrets"),
		},
		{
			name:        "secret_add",
			description: "Create a new credential entry for later use in workflow node configs. Returns a @secret:<name> reference token — the value itself is never returned.",
			schema: objSchema(map[string]interface{}{
				"kind":     strParam(`"secret" or "login"`),
				"name":     strParam("Entry name — used to build the reference token"),
				"fields":   map[string]interface{}{"type": "object", "description": "Field name/value pairs to store (values are encrypted; never returned again)"},
				"username": strParam("Optional username"),
				"url":      strParam("Optional URL"),
				"notes":    strParam("Optional notes"),
			}, "kind", "name", "fields"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("add_secret"),
		},
		{
			name:        "secret_update",
			description: "Update a credential entry's metadata or fields (values are write-only).",
			schema: objSchema(map[string]interface{}{
				"id":       strParam("Secret entry id"),
				"name":     strParam("New name (omit to leave unchanged)"),
				"username": strParam("New username (omit to leave unchanged)"),
				"url":      strParam("New URL (omit to leave unchanged)"),
				"notes":    strParam("New notes (omit to leave unchanged)"),
				"fields":   map[string]interface{}{"type": "object", "description": "Fields to add/overwrite (omit to leave unchanged)"},
			}, "id"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("update_secret"),
		},
		{
			name:        "secret_delete",
			description: "Delete a credential entry. Writes a pre-delete backup to ~/.monoagent/ai-tool-backups (fails closed if the backup can't be written).",
			schema: objSchema(map[string]interface{}{
				"id": strParam("Secret entry id"),
			}, "id"),
			annotations: map[string]bool{"destructiveHint": true},
			mutating:    true,
			handler:     chatToolHandler("delete_secret"),
		},

		// People / CRM.
		{
			name:        "person_list",
			description: "List people in the personal CRM.",
			schema: objSchema(map[string]interface{}{
				"platform": strParam("Optional platform filter"),
				"search":   strParam("Optional search term (matches username/name)"),
				"limit":    intParam("Max results (default 50)"),
			}),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("list_people"),
		},
		{
			name:        "person_get",
			description: "Get a person's full record.",
			schema: objSchema(map[string]interface{}{
				"person_id": strParam("Person ID"),
			}, "person_id"),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("get_person"),
		},
		{
			name:        "person_upsert",
			description: "Create a person, or update one if platform_username+platform already exists.",
			schema: objSchema(map[string]interface{}{
				"platform_username": strParam("Username on the platform"),
				"platform":          strParam("Platform, e.g. instagram, linkedin"),
				"full_name":         strParam("Optional full name"),
				"category":          strParam("Optional category"),
				"job_title":         strParam("Optional job title"),
				"introduction":      strParam("Optional drafted outreach message (not the profile bio)"),
			}, "platform_username", "platform"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("upsert_person"),
		},
		{
			name:        "person_delete",
			description: "Delete a person from the CRM.",
			schema: objSchema(map[string]interface{}{
				"person_id": strParam("Person ID"),
			}, "person_id"),
			annotations: map[string]bool{"destructiveHint": true},
			mutating:    true,
			handler:     chatToolHandler("delete_person"),
		},

		// Lists / templates.
		{
			name:        "social_list_list",
			description: "List saved social/contact lists.",
			schema:      objSchema(nil),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("list_social_lists"),
		},
		{
			name:        "template_list",
			global:      true, // reads no profile data
			description: "List message templates (outreach message texts), not workflow templates: those are workflow_template_list.",
			schema:      objSchema(nil),
			annotations: map[string]bool{"readOnlyHint": true},
			handler: func(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
				rt, err := s.runtime()
				if err != nil {
					return nil, err
				}
				out, err := aichat.ListMessageTemplates(rt.db.DB)
				if err != nil {
					return nil, err
				}
				return json.RawMessage(out), nil
			},
		},

		// Agent organizations (Org Runtime v2 designs).
		{
			name:        "org_list",
			description: "List the agent organizations saved in the call's profile, with each org's goal, current status, schedule, and role count.",
			schema:      objSchema(nil),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("list_orgs"),
		},
		{
			name:        "org_get",
			description: "Get one agent organization's full design: goal, run configuration, and every role in its hierarchy.",
			schema: objSchema(map[string]interface{}{
				"org_name": strParam("The org's name (its config file's name, without .json)"),
			}, "org_name"),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("get_org"),
		},
		{
			name:        "org_create",
			description: "Create a new agent organization with a single root role. Starts stopped — add more roles with org_role_add before running it. Fails if an org with this name already exists.",
			schema: objSchema(map[string]interface{}{
				"name":            strParam("Org name — used as its config filename, letters/digits/underscore/dash only"),
				"goal":            strParam("The org's overall goal/mission statement"),
				"schedule":        strParam("Optional cron-style schedule string for daemon-scheduled runs"),
				"runtime":         strParam("Optional runtime identifier for the org (omit to use the default)"),
				"workspace":       strParam("Optional workspace path the org's roles operate against"),
				"root_role_id":    strParam("Optional id for the initial root role (default: \"lead\")"),
				"root_role_title": strParam("Optional title for the initial root role (default: \"Lead\")"),
			}, "name", "goal"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("create_org"),
		},
		{
			name:        "org_role_add",
			description: "Add a new role to an org's hierarchy, reporting to an existing role. The hierarchy must stay a single-root tree.",
			schema: objSchema(map[string]interface{}{
				"org_name":         strParam("The org's name"),
				"id":               strParam("Optional explicit role id; derived from the title if omitted"),
				"title":            strParam("Display title for the role, e.g. \"Content Writer\""),
				"type":             strParam("Role type, e.g. boss, specialist, researcher, reviewer (default: specialist)"),
				"reports_to":       strParam("The id of the existing role this one reports to"),
				"responsibilities": map[string]interface{}{"type": "array", "description": "List of responsibility strings for this role", "items": map[string]interface{}{"type": "string"}},
				"model":            strParam("Optional model identifier override for this role"),
				"runtime":          strParam("Optional runtime identifier override for this role"),
				"icon":             strParam("Optional archetype icon id"),
			}, "org_name", "title", "reports_to"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("add_org_role"),
		},
		{
			name:        "org_role_update",
			description: "Update an existing role's title, type, responsibilities, model, runtime, or icon. Partial patch — fields left out are unchanged. Use org_role_set_reports_to to move a role.",
			schema: objSchema(map[string]interface{}{
				"org_name":         strParam("The org's name"),
				"role_id":          strParam("The role's id"),
				"title":            strParam("New title (omit to leave unchanged)"),
				"type":             strParam("New role type (omit to leave unchanged)"),
				"responsibilities": map[string]interface{}{"type": "array", "description": "New full list of responsibility strings, replacing the existing list (omit to leave unchanged)", "items": map[string]interface{}{"type": "string"}},
				"model":            strParam("New model identifier override; empty string to clear (omit to leave unchanged)"),
				"runtime":          strParam("New runtime identifier override; empty string to clear (omit to leave unchanged)"),
				"icon":             strParam("New archetype icon id (omit to leave unchanged)"),
			}, "org_name", "role_id"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("update_org_role"),
		},
		{
			name:        "org_role_set_reports_to",
			description: "Move a role under a different manager in an org's hierarchy. Rejects edges that would create a cycle.",
			schema: objSchema(map[string]interface{}{
				"org_name":   strParam("The org's name"),
				"role_id":    strParam("The id of the role to move"),
				"reports_to": strParam("The id of the new manager role, or empty string to make this role the root (only if the org has no other root)"),
			}, "org_name", "role_id", "reports_to"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("set_role_reports_to"),
		},
		{
			name:        "org_role_remove",
			description: "Remove a role from an org. Default strategy \"reparent\" preserves the rest of the tree. Strategy \"cascade\" deletes the role and its entire subtree — requires confirm:true; without it, only previews which roles would be deleted.",
			schema: objSchema(map[string]interface{}{
				"org_name": strParam("The org's name"),
				"role_id":  strParam("The id of the role to remove"),
				"strategy": strParam("\"reparent\" (default) or \"cascade\""),
				"confirm":  boolParam("Must be true to actually perform a cascade deletion; not required for reparent"),
			}, "org_name", "role_id"),
			annotations: map[string]bool{"destructiveHint": true},
			mutating:    true,
			handler:     chatToolHandler("remove_org_role"),
		},
		{
			name:        "org_validate",
			description: "Check an org's design against the runtime schema and structural rules. Returns valid:true/false plus any problems found. Does not modify the org.",
			schema: objSchema(map[string]interface{}{
				"org_name": strParam("The org's name"),
			}, "org_name"),
			annotations: map[string]bool{"readOnlyHint": true},
			handler:     chatToolHandler("validate_org"),
		},
		{
			name:        "org_reload",
			description: "Tell a running org's daemon to pick up design changes without restarting it. Without confirm:true, only reports whether the org is running. Requires the monomind CLI on PATH.",
			schema: objSchema(map[string]interface{}{
				"org_name": strParam("The org's name"),
				"confirm":  boolParam("Must be true to actually signal the running daemon"),
			}, "org_name"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("reload_org"),
		},
		{
			name:        "org_automation_add",
			description: "Add an existing workflow to an org as one of its automations under a short alias; roles can then be granted it with org_grant_set.",
			schema: objSchema(map[string]interface{}{
				"org_name":    strParam("The org's name"),
				"workflow_id": strParam("The workflow to add (same profile)"),
				"alias":       strParam("Lowercase alias; becomes the tool automation_<alias>"),
				"owned":       boolParam("Whether this org owns the workflow's lifecycle"),
			}, "org_name", "workflow_id", "alias"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("add_org_automation"),
		},
		{
			name:        "org_grant_set",
			description: "Grant (or with revoke:true, revoke) one org role the right to call one of the org's automations as a tool. Without confirm:true, only previews.",
			schema: objSchema(map[string]interface{}{
				"org_name":          strParam("The org's name"),
				"role_id":           strParam("The agent role receiving the tool"),
				"automation":        strParam("The automation's alias"),
				"mode":              strParam("run | trigger | status"),
				"wait":              boolParam("Wait for the run and return its output"),
				"timeout_seconds":   intParam("Seconds to wait"),
				"approval":          strParam("none | required"),
				"max_calls_per_run": intParam("Calls allowed per org run"),
				"revoke":            boolParam("Revoke instead of set"),
				"confirm":           boolParam("Must be true to apply"),
			}, "org_name", "role_id", "automation"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("set_org_grant"),
		},
		{
			name:        "org_autonomy_set",
			description: "Set who decides an org's approvals, questions, and gates: level manual, mid, or full, and the decider (model, boss, parent, or jev — TypeSafe Jev picks the verdict when its top probability reaches decider_threshold, default 0.8, else the model decider decides; needs a TypeSafe key). Without confirm:true, only previews.",
			schema: objSchema(map[string]interface{}{
				"org_name":           strParam("The org's name"),
				"level":              strParam("manual | mid | full"),
				"decider":            strParam(strings.Join(orgdesign.DeciderKinds, " | ")),
				"decider_threshold":  map[string]interface{}{"type": "number", "description": "jev decider only: lowest top-verdict probability Jev decides at, in (0,1] (default 0.8)"},
				"decider_model":      strParam("Model id for the model decider"),
				"decider_runtime":    strParam("Runtime for the model decider"),
				"policy":             strParam("Instructions the decider follows"),
				"on_decider_failure": strParam("deny | human"),
				"confirm":            boolParam("Must be true to apply"),
			}, "org_name"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     chatToolHandler("set_org_autonomy"),
		},
	}
	for i := range tools {
		tools[i].handler = requireStringArgs(tools[i].schema, tools[i].handler)
	}
	return tools
}

// emptyStringIsAValue lists the required string arguments whose empty string
// is a real answer: "reports_to" is "" to make a role the root of its org
// (org_role_add, org_role_set_reports_to). For these the key must be present
// as a string, but may be empty.
var emptyStringIsAValue = map[string]bool{"reports_to": true}

// requireStringArgs makes a missing or empty required string argument an
// explicit error. Without it a call that used the wrong key (say "id" for
// workflow_id) reached the store with an empty id and answered
// "workflow  not found".
func requireStringArgs(schema map[string]interface{}, next toolHandler) toolHandler {
	required, _ := schema["required"].([]string)
	props, _ := schema["properties"].(map[string]interface{})
	var keys []string
	for _, k := range required {
		if p, ok := props[k].(map[string]interface{}); ok && p["type"] == "string" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return next
	}
	return func(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
		var got map[string]interface{}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &got)
		}
		for _, k := range keys {
			v, present := got[k].(string)
			if !present {
				return nil, fmt.Errorf("invalid_input: the %q argument is required", k)
			}
			if v == "" && !emptyStringIsAValue[k] {
				return nil, fmt.Errorf("invalid_input: the %q argument is required", k)
			}
		}
		return next(ctx, s, args)
	}
}
