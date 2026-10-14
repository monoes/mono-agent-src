package workflow

// ScheduleTriggerSchema and WebhookTriggerSchema document the config keys
// read by activateSchedule and activateWebhook in trigger_manager.go — see
// internal/nodes/control.SetNodeSchema's doc comment (in that package) for
// why these are companion structs rather than the runtime config, and
// internal/tools/schemagen for the tag grammar. Unlike every other
// generated node type, trigger.schedule and trigger.webhook config isn't
// read inside a NodeExecutor.Execute method — it's read directly out of
// WorkflowNode.Config by the trigger manager when the trigger is activated.
//
// trigger.manual is intentionally not converted: it has no config at all
// (see schemas/trigger.manual.json — an empty fields array), so a companion
// struct would have zero tagged fields and add nothing over the existing
// hand-written file.
//
// "hmac_secret" has no entry in the hand-written schemas/trigger.webhook.json
// this replaces — activateWebhook has read node.Config["hmac_secret"] since
// it was written, but the schema never exposed a way to set it from the UI.
// Separately, the hand-written schema marks "path" as optional ("Leave
// blank to auto-generate"), but activateWebhook actually rejects an empty
// path with an error — there is no auto-generation path in this codebase.
// Generating from the struct fixes both gaps.
type ScheduleTriggerSchema struct {
	Cron string `json:"cron" schema:"label=Cron Expression,type=text,required,placeholder=e.g. 0 0 9 * * 1-5,help=6-field cron: seconds minutes hours day month weekday. e.g. '0 0 9 * * 1-5' = weekdays at 9am."`

	Timezone string `json:"timezone" schema:"label=Timezone,type=text,default=UTC,help=IANA timezone name， e.g. America/New_York"`

	Overlap string `json:"overlap" schema:"label=When a run is still going,type=select,options=allow|skip|queue_one,default=allow,help=allow: every tick starts a run. skip: drop the tick while a run of this workflow is queued， running or waiting. queue_one: keep at most one tick waiting for the current run to end."`

	CatchUp string `json:"catch_up" schema:"label=Missed runs,type=select,options=none|one|all,default=none,help=What the daemon does on startup with ticks missed while it was not running (counted from the last scheduled run). none: skip them. one: a single run. all: one run per missed tick (at most 100)."`

	CatchUpWindow string `json:"catch_up_window" schema:"label=Catch-up window,type=text,placeholder=e.g. 6h,help=Only catch up ticks missed within this long before startup (a duration such as 30m or 6h). Empty: no limit."`
}

// The run's trigger item for a webhook is built by buildWebhookItem
// (webhook_request.go): body, query, headers, method and path (plus binary
// for multipart files), with a JSON or form object body's fields also copied
// to the top level, so $json.<field> works as it did before those keys
// existed. The response modes, dedupe and size options are read by
// applyWebhookOptions (webhook_config.go).
type WebhookTriggerSchema struct {
	Path string `json:"path" schema:"label=Webhook Path,type=text,required,placeholder=/webhook/my-hook,help=URL path suffix. Required — activation fails if left blank."`

	Method string `json:"method" schema:"label=HTTP Method,type=select,options=GET|POST|PUT|PATCH|DELETE,default=POST"`

	HMACSecret string `json:"hmac_secret" schema:"label=HMAC Secret,type=password,help=If set， incoming requests must include a valid HMAC signature computed with this secret."`

	AuthHeader string `json:"auth_header" schema:"label=Auth Header Name,type=text,placeholder=X-Webhook-Secret,help=If set， requests must include this header with the auth token value."`

	AuthToken string `json:"auth_token" schema:"label=Auth Token,type=password,help=Secret value expected in the auth header."`

	ResponseMode string `json:"response_mode" schema:"label=Response Mode,type=select,options=onReceived|lastNode|responseNode,default=onReceived,help=onReceived answers at once with the execution_id. lastNode waits for the run and answers with the last node's output. responseNode waits for a Respond to Webhook node."`

	ResponseCode float64 `json:"response_code" schema:"label=Response Code,type=number,default=200,min=200,max=299,depends_on_key=response_mode,depends_on_values=onReceived,help=Status of the immediate answer."`

	ResponseData string `json:"response_data" schema:"label=Response Data,type=select,options=firstEntryJson|allEntries|noData,default=firstEntryJson,depends_on_key=response_mode,depends_on_values=lastNode,help=firstEntryJson: the first item's JSON. allEntries: every item's JSON as a list. noData: an empty body."`

	ResponseTimeout float64 `json:"response_timeout" schema:"label=Response Timeout (seconds),type=number,default=30,min=1,max=300,depends_on_key=response_mode,depends_on_values=lastNode|responseNode,help=How long to hold the request. A run still going is answered 202 with its execution_id."`

	DedupeHeader string `json:"dedupe_header" schema:"label=Delivery ID Header,type=text,placeholder=X-GitHub-Delivery,help=If set， a request whose value of this header was seen in the last 7 days starts no run and is answered with the first delivery's execution_id (e.g. Idempotency-Key)."`

	MaxBodyBytes float64 `json:"max_body_bytes" schema:"label=Max Body Size (bytes),type=number,default=1048576,min=1,max=33554432,help=Largest request body accepted， multipart files included."`
}

// SSETriggerSchema documents the config keys SSETriggerSource.Activate
// reads (trigger_sse.go).
type SSETriggerSchema struct {
	URL string `json:"url" schema:"label=URL,type=text,required,placeholder=https://example.com/events,help=The text/event-stream endpoint. Every event starts one run with {event， data， id}."`

	Headers string `json:"headers" schema:"label=Headers (JSON),type=textarea,placeholder={ \"Authorization\": \"Bearer @secret:feed_token\" },help=Request headers as a JSON object. A value that is an @secret:<name> reference is read from the vault."`
}
