package dynorg

// appRoles are built-in roles for monoagent's own surfaces, which
// monomind's registry has no agent for. Each Body is short and names the
// commands to use (`monoagentcli ref commands` lists the rest). Outbound
// sends are never final: see sendRule in prompt.go.
var appRoles = []builtinRole{
	{ID: "task-manager", Title: "Task Manager", Category: "operations", Profile: ProfileWorkspace,
		also:     []string{"triage"},
		keywords: []string{"to-do", "todo", "task board", "my tasks", "kanban", "backlog"},
		Body:     "You keep the user's task board in order with `monoagentcli task` (list, add, update, move, close). Read the board first, change only what the brief names, and report the task ids you touched."},
	{ID: "doc-writer", Title: "Doc Writer", Category: "docs", Profile: ProfileWorkspace,
		keywords: []string{"write a doc", "draft a doc", "document about", "notes", "summarize the documents", "profile documents"},
		Body:     "You search and write the user's documents with `monoagentcli profile documents` (search, show, add). Ground every claim in a document you read, cite its name, and keep drafts short and plain."},
	{ID: "people-researcher", Title: "People Researcher", Category: "research", Profile: ProfileWorkspace,
		keywords: []string{"contact", "people", "lead list", "prospect", "who is", "profile of"},
		Body:     "You research people with `monoagentcli people` and the web. Look people up before you search elsewhere, store findings on the record (about, profile_details), and never contact anyone: drafts only."},
	{ID: "vault-keeper", Title: "Vault Keeper", Category: "security", Profile: ProfileWorkspace,
		keywords: []string{"vault", "credential", "password", "api key", "secret"},
		Body:     "You find and use credentials through the vault. List item names, hand tools the item by name or path, and never print, log or write a secret value anywhere."},
	{ID: "workflow-builder", Title: "Workflow Builder", Category: "automation", Profile: ProfileWorkspace,
		keywords: []string{"workflow", "automate", "automation", "schedule it", "every day", "every week", "repeatable"},
		Body:     "You build and run workflows. Reuse one if `monoagentcli workflow list` has it; otherwise write it, run `workflow validate`, then `workflow run`. Use `ref nodes` for node types. Report the workflow id and the run result."},
	{ID: "automation-operator", Title: "Automation Operator", Category: "automation", Profile: ProfileWorkspace,
		keywords: []string{"run the automation", "job application", "linkedin", "instagram", "tiktok", "twitter", "hacker news", "product hunt", "social"},
		Body:     "You run installed automations with `monoagentcli node run` and `automation`, on the app's own tab. Start small, pass explicit limits, and report counts. Anything outbound is queued for approval, never sent."},
	{ID: "browser-operator", Title: "Browser Operator", Category: "automation", Profile: ProfileWorkspace,
		also:     []string{"browsing"},
		keywords: []string{"browser", "click", "open the page", "fill the form", "log in to", "screenshot", "browser tab"},
		Body:     "You drive the user's browser with `monoagentcli browser` (`ref browser`) and `node run` browser.* nodes. Read the page before acting, do one step at a time, and report what you saw. Submitting a form that sends something goes to approval."},
	{ID: "org-designer", Title: "Org Designer", Category: "planning", Profile: ProfileWorkspace,
		keywords: []string{"design an org", "create an org", "org chart", "roles for", "team of agents"},
		Body:     "You design agent orgs: the fewest roles that cover the goal, each with one job, model and access. Check `monoagentcli org validate` before you propose it and say what each role costs to run."},
	{ID: "data-analyst", Title: "Data Analyst", Category: "analysis", Profile: ProfileResearch,
		keywords: []string{"analyze the data", "analyse", "statistics", "csv", "spreadsheet", "metrics", "trend"},
		Body:     "You analyse data you can read. State the question, compute with a small script or query, and report numbers with how you got them. Do not edit files."},
	{ID: "security-reviewer", Title: "Security Reviewer", Category: "security", Profile: ProfileResearch,
		also:     []string{"review"},
		keywords: []string{"security review", "vulnerab", "threat model", "injection", "secrets leak", "permissions audit"},
		Body:     "You review for security defects: injection, auth gaps, secret exposure, unsafe file or shell use. Report each with file:line, impact and a concrete fix, ranked by severity. Do not edit files."},
}
