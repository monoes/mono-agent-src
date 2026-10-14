package catalog

// Node reference entries (`monoagentcli ref node <type>`), one variable per
// category; catalog.go joins them into NodeDocs.

// nodeDocsPublication is the "publication" category of the node catalogue.
var nodeDocsPublication = []nodeDoc{
	{Type: "publication.register", Category: "publication", Short: "Record a successful publication from a custom publisher", Description: "Registers already-published content in this profile; it does not publish. Use after custom HTTP/code/shell publishers. Built-in publishers record automatically.", Config: `{ "platform": "my-blog", "kind": "article", "body": "{{ $json.text }}", "url": "{{ $json.url }}" }`, Inputs: "items with publication fields (error/success:false items are skipped)", Outputs: "registered publication records", Notes: "Use a stable idempotency_key to retry registration safely. See ref publication."},
}

// nodeDocsApplications is the "applications" category of the node catalogue.
var nodeDocsApplications = []nodeDoc{
	{
		Type:     "discovery.search_jobs",
		Category: "applications",
		Short:    "Search a job board and save new postings as pending Applications",
		Description: `Searches one job board and, for every non-duplicate result, saves it
directly as a new "pending" job Application — this node has side effects,
it does not just return search results. Safe to re-run repeatedly on the
same keywords: duplicate detection (by URL) skips postings already saved.

Each source has different location semantics: "linkedin" and "arbeitnow"
accept free-text city/country names; "jobicy" only accepts a small set of
predefined region/country slugs (e.g. "germany", "europe", "usa") — an
unrecognized value is dropped rather than failing the search, since Jobicy
is remote-jobs-only anyway. linkedin additionally respects robots.txt and
will fail outright if the endpoint is disallowed at request time; use
on_error: "continue" (workflow-level, not a config field) so one source
failing doesn't block the others when searching several in parallel — see
the bundled "find_jobs" template for the pattern.`,
		Config: `{
  "keywords": "backend engineer",
  "location": "Berlin",
  "source":   "arbeitnow",
  "limit":    20
}`,
		Inputs:  "none (query comes from config, usually templated from trigger data)",
		Outputs: "one item per new, non-duplicate posting: id, kind (\"job\"), status (\"pending\"), title, company, url",
		Notes:   "source: linkedin | arbeitnow | jobicy (default linkedin). limit is capped at 100. profile_id defaults to \"default\".",
	},
	{
		Type:     "applications.create",
		Category: "applications",
		Short:    "Manually create one job or tender Application",
		Description: `Creates a single Application from config (not from input items — one
call makes one Application). Use discovery.search_jobs instead when
importing postings from a job board; use this node for tenders (no
discovery source exists for those yet) or one-off manual entries.`,
		Config: `// kind = "job"
{ "kind": "job", "title": "Backend Engineer", "company": "Acme", "url": "https://example.com/jobs/1", "location": "Berlin", "source": "manual" }

// kind = "tender"
{ "kind": "tender", "title": "IT Services RFP 2026", "issuing_org": "City of Berlin", "url": "https://example.com/rfp/1", "submission_deadline": "2026-12-01" }`,
		Inputs:  "none (all fields come from config)",
		Outputs: "id, kind, status (\"pending\"), profile_id, created_at, updated_at",
		Notes:   "kind must be \"job\" or \"tender\". company is expected for job, issuing_org for tender, though not enforced at the node level. profile_id defaults to \"default\".",
	},
	{
		Type:     "applications.list",
		Category: "applications",
		Short:    "List Applications, optionally filtered by kind/status/tag",
		Config:   `{ "kind": "job", "status": "pending", "tag": "" }`,
		Inputs:   "none",
		Outputs:  "one item per matching Application (full record, including Job or Tender details)",
		Notes:    "Leave kind/status/tag blank to include everything. profile_id defaults to \"default\".",
	},
	{
		Type:     "applications.set_status",
		Category: "applications",
		Short:    "Transition an Application to rejected or cancelled",
		Config:   `{ "id": "{{ $json.id }}", "status": "rejected", "actor": "system", "note": "no response after 30 days" }`,
		Inputs:   "item with the application id (or hardcode id in config)",
		Outputs:  "id, status, updated_at",
		Notes:    `Only "rejected" and "cancelled" are valid here — moving an Application to "applied" always requires the explicit human action behind 'monoagentcli application send', never this node. Must be a legal transition from the Application's current status or the node errors.`,
	},
	{
		Type:     "applications.tag",
		Category: "applications",
		Short:    "Add or remove a tag on an Application",
		Config:   `{ "id": "{{ $json.id }}", "tag": "high-priority", "action": "add" }`,
		Inputs:   "item with the application id",
		Outputs:  "id, tags (full updated tag list)",
		Notes:    "action: add (default) | remove.",
	},
	{
		Type:     "applications.evaluate",
		Category: "applications",
		Short:    "Score a job Application's fit against your profile using a local AI agent",
		Description: `Runs a local agent (via the monomind engine, same runtimes as 'monoagentcli
agent'/'chat') to compare the job posting against your profile data and
produce a fit verdict. Needs a working local agent runtime — see
'monoagentcli agent' and 'monoagentcli chat' to verify one is installed
before relying on this in an unattended workflow.`,
		Config:  `{ "application_id": "{{ $json.id }}", "runtime": "claude" }`,
		Inputs:  "item with application_id (or hardcode in config)",
		Outputs: "application_id, verdict, overall_score, rationale",
		Notes:   "runtime defaults to \"claude\". profile_id defaults to \"default\". Only meaningful for kind=job Applications.",
	},
	{
		Type:     "applications.prepare",
		Category: "applications",
		Short:    "Generate (or reuse) an Application's CV and cover letter documents",
		Description: `Renders a CV and cover letter tailored to one Application and stores
both as HTML+PDF vault documents linked to it. Re-running with the same
application_id reuses/regenerates rather than endlessly duplicating —
see 'monoagentcli application apply' for the CLI command that wraps this
plus opens the posting in a browser (deliberately not itself a node —
an unattended workflow should never pop open an interactive browser
window as a side effect).`,
		Config: `{
  "application_id": "{{ $json.id }}",
  "cv_data": { "name": "Jane Doe", "summary": "...", "experience": [] },
  "cover_letter_data": { "recipient": "Hiring Manager", "body": "..." }
}`,
		Inputs:  "item with application_id",
		Outputs: "application_id, cv_html_document_id, cv_pdf_document_id, cover_letter_html_document_id, cover_letter_pdf_document_id",
		Notes:   "cv_data / cover_letter_data must match documents.CVData / documents.CoverLetterData shapes — see documents.render for the same underlying templates.",
	},
	{
		Type:     "documents.render",
		Category: "applications",
		Short:    "Render a CV, cover letter, or tender proposal from structured JSON data",
		Config: `{
  "doc_type": "cv",
  "data": { "name": "Jane Doe", "summary": "...", "experience": [] },
  "application_id": "{{ $json.id }}"
}`,
		Inputs:  "none (data comes from config)",
		Outputs: "document_id (HTML), pdf_document_id",
		Notes:   "doc_type: cv | cover_letter | tender_proposal. application_id is optional — link the generated document to an existing Application, or omit for a standalone document. profile_id defaults to \"default\".",
	},
}

// nodeDocsPeople is the "people" category of the node catalogue.
var nodeDocsPeople = []nodeDoc{
	{
		Type:     "people.save",
		Category: "people",
		Short:    "Save scraped profiles to the people database",
		Config:   `{}`,
		Inputs:   "items with: url, platform, username, display_name, bio",
		Outputs:  "saved_count",
	},
	{
		Type:     "people.sync_outlook_message",
		Category: "people",
		Short:    "Upsert People + message history from service.outlook_mail's list_messages output",
		Description: `Feed this directly from a service.outlook_mail list_messages node's output.
For each message item, upserts a Person (platform="email") and a
PersonMessage attached to them — this is how Outlook mail becomes visible
on a person's Profile page / in the Communications feed / GetPersonMessages.

"direction" controls WHICH side of the message becomes the person:
  inbound  (default) — the SENDER becomes the person. Use with an inbox read.
  outbound            — each RECIPIENT becomes the person instead (the sender
                         is always your own mailbox for sent mail, so syncing
                         it as "the person" would be wrong). Use with a
                         sentitems read. Requires the upstream list_messages
                         node to select toRecipients — it's included in
                         service.outlook_mail's default $select already.

To sync BOTH received and sent mail, wire up TWO independent branches off
the same trigger: one inbox read → sync (direction=inbound), one sentitems
read → sync (direction=outbound). This is exactly what the bundled
"outlook_email_sync" workflow template does — run
'monoagentcli workflow templates use outlook_email_sync' to get both
branches pre-wired instead of building them by hand.

"profile_id" defaults to "default" if omitted — this is EASY to get wrong:
it is NOT inherited from the workflow's own profile automatically. A
workflow belonging to profile X whose sync node has no explicit profile_id
will silently attribute all synced people/messages to the "default"
profile instead of X. Always set profile_id explicitly to match the
workflow's profile (monoagentcli profile list to find the ID).`,
		Config: `// Inbound (received mail) — sender becomes the person
{ "source": "outlook", "direction": "inbound", "profile_id": "680bc228-81c9-40e6-aedc-52b6fdb261e0" }

// Outbound (sent mail) — recipient(s) become the person
{ "source": "outlook", "direction": "outbound", "profile_id": "680bc228-81c9-40e6-aedc-52b6fdb261e0" }`,
		Inputs:  "items from service.outlook_mail's list_messages output (id, subject, from, toRecipients, body/bodyPreview, receivedDateTime, plus its _source and attachments)",
		Outputs: "one item per synced person/message: {person_id, email, message_id}",
		Notes: "Stores each message's provenance (_source: system, account, folder, id there, web link, " +
			"synced at) and its attachment records (filename + local path) in person_messages.metadata. " +
			"Read them back with `monoagentcli people messages show <message-id>` — attachment paths are " +
			"real files you can open.",
	},
}
