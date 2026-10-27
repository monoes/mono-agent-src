package chat

// ---------------------------------------------------------------------------
// Tool definitions
// ---------------------------------------------------------------------------

func strParam(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

func boolParam(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "boolean", "description": desc}
}

func intParam(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "integer", "description": desc}
}

// ToolDefs returns every monoagent-domain tool the model can call.
func (mt *MonoagentTools) ToolDefs() []ToolDef {
	def := func(name, desc string, props map[string]interface{}, required []string) ToolDef {
		params := map[string]interface{}{"type": "object", "properties": props}
		if len(required) > 0 {
			params["required"] = required
		}
		return ToolDef{Type: "function", Function: ToolFunction{Name: name, Description: desc, Parameters: params}}
	}

	defs := []ToolDef{
		// Workflows
		def("list_workflows", "List all workflows in the active profile", nil, nil),
		def("get_workflow", "Get a workflow's metadata, nodes, and connections", map[string]interface{}{
			"workflow_id": strParam("The workflow ID"),
		}, []string{"workflow_id"}),
		def("delete_workflow", "Permanently delete a workflow and its nodes/connections/executions", map[string]interface{}{
			"workflow_id": strParam("The workflow ID"),
		}, []string{"workflow_id"}),
		def("set_workflow_active", "Activate or deactivate a workflow (active workflows run on their triggers)", map[string]interface{}{
			"workflow_id": strParam("The workflow ID"),
			"active":      boolParam("true to activate, false to deactivate"),
		}, []string{"workflow_id", "active"}),
		def("run_workflow", runWorkflowDescription(mt.runsAllowed())+" Pass confirm:true to actually execute (a preview without it); execution additionally requires the session to have been started with runs explicitly enabled.", map[string]interface{}{
			"workflow_id": strParam("The workflow ID"),
			"confirm":     boolParam("Must be true to actually execute; omit/false to preview"),
		}, []string{"workflow_id"}),
		def("create_workflow", "Create a new, empty workflow to add nodes to. This is the only way to build a workflow from chat — add nodes to it afterward with add_workflow_node, then run it with run_workflow.", map[string]interface{}{
			"name":        strParam("Workflow name"),
			"description": strParam("Optional description"),
		}, []string{"name"}),
		def("list_node_types", "List available workflow node types, optionally filtered by category (e.g. instagram, linkedin, x, tiktok, http, control, data). Call this before add_workflow_node if you don't already know the exact node_type string to use.", map[string]interface{}{
			"category": strParam("Optional category filter"),
		}, nil),
		def("add_workflow_node", "Add a node to a workflow. node_type is \"<platform>.<action>\" for social-platform automation (e.g. \"instagram.like_posts\", \"linkedin.send_dms\" — see list_node_types), or a plain type for other categories (e.g. \"http.request\"). config is a JSON object of that node type's settings — common browser-automation fields are username/credential_id (session identity), targets (list of profile URLs/usernames to act on), keywords, message.", map[string]interface{}{
			"workflow_id": strParam("The workflow ID to add the node to"),
			"node_type":   strParam("The node type, e.g. instagram.like_posts"),
			"name":        strParam("A human-readable name for this node"),
			"config":      map[string]interface{}{"type": "object", "description": "Node configuration matching the node type's schema"},
		}, []string{"workflow_id", "node_type", "name"}),

		// Vault (media: images, audio, video, and other files)
		def("list_vault_items", "List the media vault: images, audio, video and other files stored in it (each has a kind: image, audio or video)", map[string]interface{}{
			"limit": intParam("Max items to return (default 50)"),
		}, nil),
		def("get_vault_item_path", "Resolve a vault item id to its filesystem path", map[string]interface{}{
			"vault_id": strParam("Vault item id, e.g. img-001"),
		}, []string{"vault_id"}),
		def("search_profile_documents", "Search the user's uploaded profile documents (résumé, cover letters, etc.) for relevant content.", map[string]interface{}{
			"query": strParam("Search query"),
		}, []string{"query"}),
		def("save_document", "Save generated content — a report, summary, write-up, or other deliverable — as a real file in this profile's docs/ folder. It appears in the Documents vault within a few seconds. Use this whenever you're asked to produce, deliver, or hand off a document; don't just print long deliverable content inline in chat. Only "+allowedDocumentExtensionsText()+" filenames are accepted (matches what the Documents vault indexes). For a styled report, use either .md, or a self-contained .html file with inline <style> and no external resources — the vault preview renders it in a sandboxed frame that strips out any <script>, so treat it as static markup only, never something that needs JavaScript to display correctly. Include the topic and date in the filename (e.g. \"iran-war-debate-2026-09-11.md\") since an exact-duplicate filename is refused rather than overwritten.", map[string]interface{}{
			"filename": strParam("File name including extension, e.g. \"iran-war-debate-2026-09-11.md\". Any directory portion is stripped — it always lands directly in docs/."),
			"content":  strParam("The full file content to write, as UTF-8 text."),
		}, []string{"filename", "content"}),
		def("save_image", "Save an image — a generated graphic, banner, photo, icon, or diagram — into this profile's media vault so it appears in the Media Vault immediately. Provide the filename (e.g. \"banner.png\", \"icon.svg\") and image content (base64-encoded image bytes, SVG markup, or a local file path). Supported formats: png, jpg, jpeg, gif, webp, svg, bmp, ico.", map[string]interface{}{
			"filename": strParam("Image filename with extension (e.g. \"header.png\", \"diagram.svg\"). Any directory portion is stripped."),
			"content":  strParam("Image data: base64-encoded binary data (with or without data:image/...;base64, prefix), raw SVG markup, or local file path."),
			"label":    strParam("Optional human-readable label or description for the image."),
		}, []string{"filename", "content"}),

		// Credentials vault — metadata/reference only, never returns values
		def("list_secrets", "List credential entries by name and metadata only. Values are never returned by any tool.", nil, nil),
		def("add_secret", "Create a new credential entry for later use in workflow node configs. Build a reference by joining an at-sign, the word secret, a colon, and this entry's name; the workflow executor resolves it at run time and the value itself is never shown to you.", map[string]interface{}{
			"kind":     strParam(`"secret" or "login"`),
			"name":     strParam("Entry name — used to build the reference token"),
			"fields":   map[string]interface{}{"type": "object", "description": "Field name/value pairs to store (values are encrypted; you will never see them again)"},
			"username": strParam("Optional username"),
			"url":      strParam("Optional URL"),
			"notes":    strParam("Optional notes"),
		}, []string{"kind", "name", "fields"}),
		def("update_secret", "Update a credential entry's metadata or fields (values are write-only)", map[string]interface{}{
			"id":       strParam("Secret entry id"),
			"name":     strParam("New name (omit to leave unchanged)"),
			"username": strParam("New username (omit to leave unchanged)"),
			"url":      strParam("New URL (omit to leave unchanged)"),
			"notes":    strParam("New notes (omit to leave unchanged)"),
			"fields":   map[string]interface{}{"type": "object", "description": "Fields to add/overwrite (omit to leave unchanged)"},
		}, []string{"id"}),
		def("delete_secret", "Delete a credential entry", map[string]interface{}{
			"id": strParam("Secret entry id"),
		}, []string{"id"}),

		// People
		def("list_people", "List people in the personal CRM", map[string]interface{}{
			"platform": strParam("Optional platform filter"),
			"search":   strParam("Optional search term (matches username/name)"),
			"limit":    intParam("Max results (default 50)"),
		}, nil),
		def("get_person", "Get a person's full record", map[string]interface{}{
			"person_id": strParam("Person ID"),
		}, []string{"person_id"}),
		def("upsert_person", "Create a person, or update one if platform_username+platform already exists", map[string]interface{}{
			"platform_username": strParam("Username on the platform"),
			"platform":          strParam("Platform, e.g. instagram, linkedin"),
			"full_name":         strParam("Optional full name"),
			"category":          strParam("Optional category"),
			"job_title":         strParam("Optional job title"),
			"introduction":      strParam("Optional drafted outreach message (not the profile bio)"),
		}, []string{"platform_username", "platform"}),
		def("delete_person", "Delete a person from the CRM", map[string]interface{}{
			"person_id": strParam("Person ID"),
		}, []string{"person_id"}),

		// Communications
		def("list_messages", "List message/interaction history, optionally for one person", map[string]interface{}{
			"person_id": strParam("Optional: limit to one person"),
			"limit":     intParam("Max results (default 50)"),
		}, nil),
		def("get_message", "Get a single message's full body", map[string]interface{}{
			"message_id": strParam("Message ID"),
		}, []string{"message_id"}),

		// Lists / templates
		def("list_social_lists", "List saved social/contact lists", nil, nil),
		def("list_templates", "List message templates", nil, nil),

		// Agent organizations (Org Runtime v2 designs)
		def("list_orgs", "List the agent organizations saved in the active profile, with each org's goal, current status, schedule, and how many roles it has. Use this before get_org when you don't already know the exact org name.", nil, nil),
		def("get_org", "Get one agent organization's full design: its goal, run configuration, and every role in its hierarchy with title, type, reports_to (the id of its manager, or null for the root), and responsibilities. Call this before editing an org so you know the current role ids and structure.", map[string]interface{}{
			"org_name": strParam("The org's name (its config file's name, without .json)"),
		}, []string{"org_name"}),
		def("create_org", "Create a new agent organization with a single root role. The org starts stopped — add more roles with add_org_role before running it. Fails if an org with this name already exists (no silent overwrite).", map[string]interface{}{
			"name":            strParam("Org name — used as its config filename, letters/digits/underscore/dash only"),
			"goal":            strParam("The org's overall goal/mission statement"),
			"schedule":        strParam("Optional cron-style schedule string for daemon-scheduled runs; omit for a manually-run org"),
			"runtime":         strParam("Optional runtime identifier for the org (omit to use the default)"),
			"workspace":       strParam("Optional workspace path the org's roles operate against"),
			"root_role_id":    strParam("Optional id for the initial root role (default: \"lead\")"),
			"root_role_title": strParam("Optional title for the initial root role (default: \"Lead\")"),
		}, []string{"name", "goal"}),
		def("add_org_role", "Add a new role to an org's hierarchy, reporting to an existing role. The hierarchy must stay a single-root tree — reports_to must name a role that already exists in the org (get_org first if unsure), or be an empty string only when this is meant to become a NEW root, which is only valid if the org has no root role yet (a normal org already has one — use set_role_reports_to to move an existing role instead of creating a second root).", map[string]interface{}{
			"org_name":         strParam("The org's name"),
			"id":               strParam("Optional explicit role id; if omitted, one is derived from the title. If given and already in use, the call fails rather than silently renaming it."),
			"title":            strParam("Display title for the role, e.g. \"Content Writer\""),
			"type":             strParam("Role type, e.g. boss, specialist, researcher, reviewer (default: specialist)"),
			"reports_to":       strParam("The id of the existing role this one reports to; empty string only to create a second root (almost always wrong — see description)"),
			"responsibilities": map[string]interface{}{"type": "array", "description": "List of responsibility strings for this role", "items": map[string]interface{}{"type": "string"}},
			"model":            strParam("Optional model identifier override for this role"),
			"runtime":          strParam("Optional runtime identifier override for this role"),
			"icon":             strParam("Optional archetype icon id (e.g. \"security-auditor\", \"backend-dev\", \"coder\") — not just cosmetic: matching a real bundled archetype id automatically injects that archetype's researched best-practices into this role's briefing at runtime. See the createorg skill guidance for the id list; leave unset rather than guessing an id that doesn't exist."),
		}, []string{"org_name", "title", "reports_to"}),
		def("update_org_role", "Update an existing role's title, type, responsibilities, model, runtime, or icon. Any field left out of the call is unchanged — this is a partial patch, not a full replace. To move a role to a different manager, use set_role_reports_to instead; this tool does not change reports_to.", map[string]interface{}{
			"org_name":         strParam("The org's name"),
			"role_id":          strParam("The role's id"),
			"title":            strParam("New title (omit to leave unchanged)"),
			"type":             strParam("New role type (omit to leave unchanged)"),
			"responsibilities": map[string]interface{}{"type": "array", "description": "New full list of responsibility strings, replacing the existing list (omit to leave unchanged)", "items": map[string]interface{}{"type": "string"}},
			"model":            strParam("New model identifier override; pass an empty string to clear it (omit to leave unchanged)"),
			"runtime":          strParam("New runtime identifier override; pass an empty string to clear it (omit to leave unchanged)"),
			"icon":             strParam("New archetype icon id — see add_org_role's icon parameter for why this is worth setting deliberately, not just cosmetic (omit to leave unchanged)"),
		}, []string{"org_name", "role_id"}),
		def("set_role_reports_to", "Move a role under a different manager in an org's hierarchy. The hierarchy must stay a tree — an edge that would create a cycle (e.g. moving a role under one of its own descendants) is rejected with an explanation of the existing chain that would be broken. Call validate_org afterward if you're unsure the result is still a single-root tree.", map[string]interface{}{
			"org_name":   strParam("The org's name"),
			"role_id":    strParam("The id of the role to move"),
			"reports_to": strParam("The id of the new manager role, or an empty string to make this role the root — only allowed if the org has no other root role"),
		}, []string{"org_name", "role_id", "reports_to"}),
		def("remove_org_role", "Remove a role from an org. By default (strategy \"reparent\") the removed role's direct reports are re-parented to its own manager, so the rest of the tree is preserved. Pass strategy \"cascade\" to delete the role and its entire subtree instead — since that also destroys every role beneath it, cascade additionally requires confirm:true; without confirm:true it only reports which roles would be deleted and does not remove anything.", map[string]interface{}{
			"org_name": strParam("The org's name"),
			"role_id":  strParam("The id of the role to remove"),
			"strategy": strParam("\"reparent\" (default) or \"cascade\""),
			"confirm":  boolParam("Must be true to actually perform a cascade deletion; omit/false to preview which roles would be removed. Not required for the default reparent strategy."),
		}, []string{"org_name", "role_id"}),
		def("validate_org", "Check an org's design against the runtime schema and structural rules (unique role ids, exactly one root, every reports_to resolving to a real role, no cycles). Returns valid:true/false plus a list of any problems found — this does not modify the org.", map[string]interface{}{
			"org_name": strParam("The org's name"),
		}, []string{"org_name"}),
		def("reload_org", "Tell a running org's daemon to pick up design changes (new/edited/removed roles) without restarting it. Without confirm:true this only reports whether the org is currently running and does not touch the daemon — pass confirm:true to actually signal it. Reloading only matters for an org that is running; a stopped org already reflects its latest saved design the next time it starts.", map[string]interface{}{
			"org_name": strParam("The org's name"),
			"confirm":  boolParam("Must be true to actually signal the running daemon; omit/false to only check whether it's running"),
		}, []string{"org_name"}),
	}
	defs = append(defs, orgUnificationToolDefs(def)...)
	defs = append(defs, publicationToolDefs()...)
	if mt.pageRead() {
		// The proposal tools exist only here: their answers are cards only
		// the side panel draws.
		defs = append(defs, proposalToolDefs()...)
		kept := make([]ToolDef, 0, len(PageReadToolNames))
		for _, d := range defs {
			if pageReadAllows(d.Function.Name) {
				kept = append(kept, d)
			}
		}
		return kept
	}
	return defs
}

// runWorkflowDescription reflects the session's mechanical run gate in the
// tool surface itself, so the model learns refusal is structural before it
// spends a call discovering it.
func runWorkflowDescription(runsAllowed bool) string {
	if runsAllowed {
		return "Manually trigger a workflow run — it can drive real automation."
	}
	return "Manually trigger a workflow run. Execution is disabled in this session: every call will be refused until the chat is restarted with runs explicitly enabled."
}
