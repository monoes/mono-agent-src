package ref

import (
	"fmt"

	"github.com/spf13/cobra"
)

func refAPICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "api",
		Short: "HTTP/REST API surface (monoagentcli httpapi) and the OpenAI-compatible /v1 API — endpoints, auth, status codes",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║          monoagentcli — HTTP API (monoagentcli httpapi)            ║
╚══════════════════════════════════════════════════════════════╝

  monoagentcli httpapi    # REST/JSON server (default 127.0.0.1:9322)

For agents that can't speak stdio JSON-RPC. Same surface as the MCP
tools (internal/httpapi/ mirrors internal/mcp/'s conventions), plus
GET /profiles/{profile}/workflows/{id}/executions and POST
/profiles/{profile}/workflows/{id}/activate / deactivate. The server
picks no profile: workflows and HIL items are addressed per profile,
{profile} being a profile's id or name.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
ENDPOINTS
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Always registered (read-only):
    GET  /health
         unauthenticated liveness/version check
    GET  /profiles/{profile}/workflows
         list saved workflows
    GET  /profiles/{profile}/workflows/{id}
         full workflow JSON document
    GET  /profiles/{profile}/workflows/{id}/executions
         recent executions (?limit=)
    POST /profiles/{profile}/workflows/{id}/validate
         validate saved or inline workflow
    GET  /nodes
         list all node types (any profile's credential)
    GET  /nodes/{type}/schema
         embedded JSON schema for one node type
    GET  /profiles/{profile}/hil
         pending Human-in-Loop queue

  Only registered with --allow-mutations / MONOAGENT_HTTPAPI_ALLOW_MUTATIONS=1:
    POST /profiles/{profile}/workflows/{id}/run
         trigger a workflow, wait for completion
    POST /profiles/{profile}/workflows/{id}/activate
         activate a workflow's triggers
    POST /profiles/{profile}/workflows/{id}/deactivate
         deactivate a workflow's triggers
    POST /profiles/{profile}/hil/{id}/approve
         approve a pending HIL item
    POST /profiles/{profile}/hil/{id}/reject
         reject a pending HIL item
         (both 403 for a browser form submit, a site an agent asked to
         use, a send, and a workflow step whose approval sends a message
         to a person: the user decides those with hil approve|reject in
         their own terminal or on the Human in Loop page)

  Served by monoagentcli daemon (on by default), always, with its own auth:
    POST /org-endpoint/{id}               deliver an org message to an
                                          automation role (the id is the
                                          capability; see ref org)

  An unregistered mutating path 404s, not 403s — a probe can't tell
  "opted out" from "doesn't exist". Full spec: internal/httpapi/openapi.yaml
  (OpenAPI 3, validated in CI). Curl walkthrough: examples/httpapi-quickstart.md

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
AUTH
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Every request needs "Authorization: Bearer <credential>" except GET
  /health. Each profile has its own credential, stored in that profile's
  secrets vault (monoagentcli --profile <p> secret list, name
  "httpapi-token"); the user prints it with monoagentcli --profile <p>
  secret reveal httpapi-token --reveal in their own terminal (an agent, the
  chat or a workflow is refused); the server creates it for every profile
  when it starts, and reads its token field: the user sets one themselves
  with secret update httpapi-token --field token=<value> (an agent, the
  chat or a workflow is refused). A credential authorizes only the profile it belongs to: the one
  in the request path. A credential of another profile, a profile that does
  not exist and a missing credential all get the same 401, so a caller
  cannot find out which profiles exist. /nodes* takes the credential of any
  profile. A profile name that two profiles share is answered 409 to the
  holder of one of their credentials: use the id. Loopback-only bind by
  default (127.0.0.1:9322) — override with --addr or MONOAGENT_HTTPAPI_ADDR.

  The unprefixed /workflows* and /hil* routes of earlier releases are gone;
  they answer 404 with a hint naming the new path.

  Workflow reads and mutations are scoped to the profile in the path,
  including when the daemon hosts this API. Another profile's workflow
  ID returns 404. A workflow with no profile ID belongs to nobody (404 for
  every profile); legacy rows and files are stamped by migration 099 and
  the file stamp. The daemon's own triggers serve all profiles.

  A standalone httpapi serves the webhook triggers of one profile at most:
  it starts a profile's engine on that profile's first run or activate
  request (read-only mode starts none), and the first engine to find the
  webhook port (MONOAGENT_WEBHOOK_ADDR, default 127.0.0.1:9321) free takes
  it, never while a daemon or another monoagent process holds it. Other
  profiles' webhook triggers are not served; monoagentcli daemon serves them
  all, and this same API (--api, on by default). It warns at start.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
OPENAI-COMPATIBLE API (/v1)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  The same process also serves standard OpenAI-style endpoints over the
  agent runtimes installed on this machine (claude, codex, antigravity,
  ...), so an OpenAI SDK or tool works with just a base URL and a key:

    GET  /v1/models                 models as <runtime>/<model> ids
    GET  /v1/models/{id}            one model
    POST /v1/chat/completions       chat, JSON or "stream": true (SSE)
    POST /v1/images/generations     images, as b64_json, from a runtime that makes them

  Model ids look like "claude/sonnet" or "codex/gpt-6-astra" (each runtime's
  own list decides: read GET /v1/models); a bare runtime ("codex") is its
  default model, and "agy" is an alias of "antigravity".
  Sampling parameters are accepted and ignored. These are rejected with 400
  unsupported_parameter: n above 1, logprobs true, an audio object, a
  non-empty functions, a function_call other than "none", function messages
  (declare functions with tools), content parts that are not text and a
  response_format other than text or json_object. Not available: image input.

  Tool calling: tools, tool_choice, tool_calls and tool messages work on
  POST /v1/chat/completions, streaming and not, on the runtimes of
  MONOAGENT_API_TOOL_RUNTIMES (default claude,codex; none switches tool calling
  off; a model of another runtime, or of a non-chat-only runtime that monomind
  cannot run read-only or whose sandbox it cannot apply (every tool turn requires
  monomind's sandbox: 403 policy_denied, nothing run, if it cannot be applied), is
  400 unsupported_parameter on tools, before anything starts; a key created with
  --context is refused tools, 403 policy_denied naming the flag that would change
  it (--context-confinement, or --confinement on a listener that is chat-only: the
  key is held to the lower of the two, or both when both are), unless that cap is
  above chat-only). GET /v1/models and api models --json (a
  TOOLS column in its table, and the MCP tool api_models_list) give the models
  that serve them "tools" among their capabilities. A request declares up to 128
  functions (name 1 to 64 characters of [A-Za-z0-9_-], unique, where a name of 55
  or more reaches monomind and the model as an alias of 54 and the client sees its
  own; description at most 16 KiB; parameters a JSON schema object of at most
  64 KiB whose root type is object, whose properties is an object of schemas and
  whose required is a list of strings, and whose root anyOf, oneOf, allOf, local $ref,
  if/then/else, dependentSchemas/dependencies/dependentRequired and const/enum
  arguments are named at the top level for monomind, which keeps
  only top-level properties, and which is folded whole into the description; an
  enum that is not a list of strings is left out of what monomind gets; while the
  tools are passed, a schema that names no property and allows free-form keys
  (additionalProperties or unevaluatedProperties true or a schema,
  patternProperties) or has a reference that cannot be followed ($dynamicRef, a
  $ref that is not local or leads nowhere) or whose const or enum (the root's, an
  allOf's or a $ref's that applies) holds a value that is not an object is 400,
  and so is one nested more than 8 levels deep or holding more than 2000
  schemas, or functions whose schemas together take more than 100,000 steps to
  read (a step: a schema read, a reference followed, a property or a listed name
  met, an enum entry compared); whatever the tool_choice, the declaration is checked and these are 400
  invalid_value naming the parameter, never what was written: more than 128
  functions; a tool that is not an object; a name that is not 1 to 64 characters
  of [A-Za-z0-9_-], one declared twice, or an alias that is another function's
  name (a name of 55 or more reaches the model as an alias of 54, and you always
  see your own); a description of more than 16 KiB; parameters of more than 64
  KiB, that are not a JSON schema object, whose root type is not object, whose
  properties is not an object or holds a property that is not a schema, or whose
  required is not a list of strings; a tool_choice that is not none, auto,
  required or a function, or that names a function that is not declared; and in
  the conversation more than 64 calls in one message, a call with an id of no
  characters or of more than 128 bytes, a call whose name is not printable ASCII
  without [ ] < > & ' " or a backtick, a call whose arguments is not a string, a
  result of more than 256 KiB, and a tool message that answers no call of an
  earlier assistant message. Other codes: 400 unsupported_parameter for a tool
  or a call of a type other than function, a tool_choice of another type, and a
  tool_choice that forces a call when no tools are declared; 400
  missing_required_parameter for a tool with no function, a tool_choice that
  names no function, and a tool message with no tool_call_id; monomind rejects
  a call whose top-level types, string enums or required names do not match what
  it was told, which never comes back: after its round cap of 10 the answer is
  200, the cap's text and finish_reason "length"; what it cannot see comes back
  unchecked), tool_choice none (no tools: no argument is named, so none of the
  refusals of the schemas applies, while the declaration is still checked),
  auto,
  required or a named function (a best-effort line), parallel_tool_calls (treated
  as false). A response carries ONE call: the turn ends, cancelled, at the model's
  first call, and the answer is an assistant message
  with one tool_calls entry (id call_<random>, function.arguments a JSON string),
  finish_reason "tool_calls", content null or what the model said first, and no
  usage; streamed, delta.tool_calls chunks, then [DONE]. The client runs the call
  and sends the conversation again with that assistant message and a message of
  role "tool" (tool_call_id, text result up to 256 KiB). No process waits and no
  slot is held meanwhile. The follow-up continues the runtime's session when a
  single-use in-memory record fits (same key, profile, model, function and its
  arguments, tools and conversation before the call; 10 minutes; ids, names and
  hashes only) and otherwise replays the transcript, which always works. A codex
  leg runs read-only (--access read); declaring tools changes no confinement class.
  Tool results are untrusted data, fenced in the prompt with every way of writing
  a turn marker or a fence tag neutralised, and so are the arguments of a call of
  the conversation, whose name must be printable ASCII without [ ] < > & '
  " or a backtick (400; an id that is not is shown as call_1, call_2, ...); a steered call is the caller's to run or not: see
  SECURITY.md. The log line adds tools=<n> leg=first|resume|replay.

  Images: POST /v1/images/generations {model, prompt, n, size, response_format}
  answers {"created":..., "data":[{"b64_json":"..."}]}: base64 only (response_format
  "url" and stream true are 400 unsupported_parameter). n is 1 to 4, size is
  auto or WxH with each side 64 to 8192, quality, style, output_format,
  background and user are ignored, the body is capped at 64 KiB. Which runtimes
  make images is a list, MONOAGENT_API_IMAGE_RUNTIMES (default codex,antigravity;
  none switches image generation off; monomind does not report it): GET /v1/models and api models --json give their
  models "capabilities":["text","image"], and a runtime of the list that runs as
  chat-only cannot make images. model is <runtime>/<model>, a runtime of the
  list, auto, or missing (the first installed runtime of the list the policy
  allows); a model that cannot make images is 400 invalid_value. The runtime must
  write the file, so the model must be sandboxed or unconfined: under
  --confinement chat-only, or for a --context key held to chat-only, it is 403
  policy_denied and says what to raise. The turn is a chat turn with a fixed
  system prompt (use your built-in image capability, do not draw it, save each
  file in ./out-<random>/, a folder made for the turn, reply with the file
  names, NO_IMAGE_TOOL on a line of its own if you cannot); afterwards, before
  the slot folder is emptied, the gateway reads the PNG, JPEG, WebP and GIF
  files at the top of that folder and of nothing else (first bytes decide, 20 MiB
  each, the first n by name), never following a link or opening a FIFO, looking
  at no more than 64 entries. NO_IMAGE_TOOL is 400 image_generation_unsupported,
  no image 502 image_generation_failed with the runtime's reply (300
  characters). The body is streamed and the client has two minutes to read it.
  A minute or two and 40,000 input tokens per
  image, on the runtime's account. "auto" picks among the image models auto may
  pick: none while --auto-confinement is chat-only, then 404 model_not_found
  saying what to raise.

  The "auto" model lets Jev pick, per request, among the models the listener
  serves (a --context key: among those its own cap allows). It is opt-in per
  profile: monoagentcli jev enable api_auto, with the machine-wide Jev key (jev key set, or
  TYPESAFE_API_KEY in the server's environment). What leaves the machine: the
  first 4,000 characters of the last user message (of an image request, its
  prompt), and each candidate's name,
  description and validated cost and latency. It picks among chat-only models
  unless the operator raised --auto-confinement (MONOAGENT_API_AUTO_CONFINEMENT;
  a prompt can steer the pick and its author need not hold the key), never above
  --confinement or a --context key's cap; api models shows what it may pick. A Jev
  failure, a timeout (8 s) or a
  doubt falls back to a rule (of the validated models the most confined, then the
  cheapest, then the fastest; with none validated, a runtime's default model),
  never to a wider set; three questions in a row without an answer stop a
  profile's questions for 30 s. The answer carries X-Monoagent-Model (the pick) and
  X-Monoagent-Auto (jev or rule). "auto" is listed after the other models. While
  the surface or the key is missing it is not listed and answers 404
  model_not_found naming what is missing; api models and api status say it too.

  Auth is an API key, not the credential above. One profile each (its
  requests run as that profile and add only that profile's knowledge), shown
  once, only its SHA-256 is stored (no vault, no keyring, so it works on a
  headless server):

    monoagentcli --profile <profile> api key create --name NAME [--context]
    monoagentcli --profile <profile> api key list [--all-profiles] | show | update | revoke
    monoagentcli --profile <profile> api models        each model with its confinement class and
                                   whether it makes images, as this shell's
                                   flags, environment and saved settings see it
    monoagentcli --profile <profile> api status        listeners, key count, reachability
    monoagentcli api config ...    the server's settings, saved in the database
                                   (see "Server settings" below)

  monoagentcli mcp has the same management as tools, for the profile each call names (a
  required "profile" argument; api_config_get, api_config_set and api_config_apply take none):
  api_key_list, api_models_list, api_status and api_config_get, and with
  --allow-mutations api_config_set, api_config_apply and api_auto_set (see
  "Server settings"). With --allow-mutations api_key_create, api_key_update
  and api_key_revoke are listed too and change no key: only the user
  creates, changes or revokes one, with api key create|update|revoke in
  their own terminal (an agent, the chat or a workflow is refused) or in
  the app.

  --context adds excerpts of the profile's own knowledge to requests made
  with the key; only chat-only models serve such a key unless the server
  raises --context-confinement. A key never opens the routes above, and
  the credential above never opens /v1.
  org teardown-profile revokes a profile's keys.

  Confinement: chat-only (claude), sandboxed (codex: writes confined to the
  turn's folder and the temp directory, reads open), unconfined (antigravity:
  native tools run as the OS user).
  --confinement chat-only|sandboxed|any (MONOAGENT_API_CONFINEMENT) caps what
  the process serves, one value for all its listeners; a model above it is
  unlisted, GET /v1/models/{id} answers 404 for it and a completion naming it
  answers 403 policy_denied. --context-confinement
  (MONOAGENT_API_CONTEXT_CONFINEMENT, default chat-only) caps what a
  --context key may use, never above that. --auto-confinement
  (MONOAGENT_API_AUTO_CONFINEMENT, default chat-only) caps what "auto" may pick.

  Exposure: /v1 is served on the main listener only while it is loopback.
  Beyond the machine use --v1-addr (MONOAGENT_API_V1_ADDR) on httpapi or
  daemon: its own listener, only /v1 and /health, TLS only off-loopback
  (MONOAGENT_API_TLS_CERT and _KEY, else a self-signed certificate that
  remote clients must trust; with the two set, a loopback bind speaks TLS
  too), default confinement chat-only. httpapi exits when it cannot start
  that listener, daemon only warns (check api status). One process per home
  serves /v1 at a time: a second httpapi serves its other routes without it,
  a second daemon is refused.

  Limits: 2 MiB body (64 KiB for an image request); 4 concurrent turns (--max-concurrent; 429 with
  Retry-After when full); 10 minute turn timeout (MONOAGENT_API_TURN_TIMEOUT);
  no CORS. The errors of the four routes are OpenAI-shaped,
  {"error":{"message","type","param","code"}}, with an X-Request-Id header;
  an unknown path or method gets the plain 404 or 405 of Go's mux.

  Server settings: what a server reads at start (--v1-addr, the TLS files, the
  three confinement classes, --max-concurrent, the turn timeout and the two
  runtime lists: ten settings) can be saved, so that a daemon the login service
  starts, which has no flags and not this shell's environment, has them:

    monoagentcli api config show     saved, in effect here, and what the running
                                     daemon started with, per setting
    monoagentcli api config set [--v1-addr A] [--tls-cert-file P]
          [--tls-key-file P] [--confinement C] [--context-confinement C]
          [--auto-confinement C] [--max-concurrent N] [--turn-timeout D]
          [--image-runtimes L] [--tool-runtimes L] [--yes] [--dry-run]
    monoagentcli api config unset <setting>... | --all [--yes] [--dry-run]
    monoagentcli daemon restart      restart the auto-start service, so that the
                                     daemon reads them

  Keys: v1_addr, tls_cert_file, tls_key_file, confinement, context_confinement,
  auto_confinement, max_concurrent, turn_timeout, image_runtimes, tool_runtimes
  (unset also takes a flag's dashes: v1-addr). A value has the syntax of its
  environment variable and is refused where the flag or the variable would be
  (exit 3, naming the setting); an empty value is not one: use unset. The saved
  layer is stricter in two ways, since it outlives the process that wrote it: no
  value may contain a control character, and a TLS file is an absolute path (a
  service starts in another folder; nothing expands a ~). They are
  one row of the settings table (api_gateway_config), machine-wide like the
  daemon, not per profile, and set keeps the settings it is not given. Order,
  per setting: flag, then environment variable, then saved, then default (the
  TLS files, the turn timeout and the runtime lists have no flag on httpapi or
  daemon; the two TLS files are one setting: if either variable is set both come
  from the environment). A server reads them when it starts and never changes
  while it runs: set saves, daemon restart applies. api models and api status
  read the same layers, so they say what a server started now would do.

  show --json: {"v":1, "environment":"shell", "settings":[{key, server_flag, env,
  saved, default, effective, source, running, running_source, state}],
  "daemon":{running, reports_settings, autostart}, "restart_needed",
  "problems":[{key, message}]}. effective and source (env, saved or default) are
  what a server started from this shell would use. running and running_source
  (flag, env, saved or default) are what the daemon's heartbeat says it started
  with, and are absent when no daemon reports the setting. state is applied,
  pending_restart (the daemon runs the saved value or the default and a start now
  would resolve another), overridden (the daemon was given a flag or a variable
  of its own: a saved value has no effect until that is removed, whether or not
  one is saved), not_serving (only v1_addr and the two TLS files: the daemon
  took the value but the dedicated listener is not up, because it could not
  bind the address or load the certificate: its log says which), not_running or
  unknown (a daemon that predates the report). set
  and unset --json print that document for the state after the change, plus
  applied, changed (keys) and widening ([{key, reason}]).

  A change that makes the server reach further needs --yes: without it, exit 3
  and the reasons, on a terminal too (there is no prompt). That is: a dedicated
  listener (v1_addr) that reaches further than the saved one, which is beyond
  this machine where there was none or a loopback one, another host beyond it,
  or every interface where it was one host (an empty host, 0.0.0.0 and [::] are
  every interface; any host name other than localhost is a host, and a name and
  its address are two; the port alone changes nothing); a higher class of
  confinement, context_confinement or auto_confinement, on a listener on this
  machine or on one beyond it (both kinds are judged whether or not a listener
  is saved, since the daemon's own environment may name one: so raising
  confinement to sandboxed or any always needs --yes, and chat-only never does);
  a runtime list that gains a runtime it did not have (one of the default list
  that a saved list left out counts when it comes back) or leaves none;
  the removal of a saved row that cannot be read (key saved_settings, below).
  Narrowing never needs it, nor do max_concurrent, turn_timeout or the TLS
  files. Unsetting a value that was below its default (confinement chat-only, a
  list none or codex) is a widening like any other. --dry-run says what a change
  would do and whether it needs --yes, and saves nothing.

  A saved row that cannot be read (not JSON, a version that is not a whole
  number, a field of the wrong type) stops show, set, unset <setting>, httpapi,
  api models and api status (the daemon starts without the API instead) with
  exit 3 and one message that starts "the saved settings are damaged" and names
  the repair: unset --all --yes removes the
  row and says so (removed_unreadable_row in --json, a note on stderr). It needs
  --yes because what the row limited cannot be told, so removing it may reach
  further (the reason, key saved_settings; without --yes, exit 3 and that
  reason). A row written by a newer version is exit 1 and is never removed, by
  unset --all --yes either: use that version, or remove the row by hand, with
  sqlite3 ~/.monoagent/monoagent.db "delete from settings where key =
  'api_gateway_config'" (the database --db-path names, if you gave one).

  daemon restart restarts the daemon through the service it is registered as
  (launchd, systemd --user, the Windows Scheduled Task; see daemon install), says
  first, on stderr, that this interrupts what the daemon is running (workflows,
  org runs), and with --json prints {"restarted":true,"via":"launchd"}. A daemon
  that is not registered is not restarted by it: exit 3, stop it and start it
  again. If a daemon was started by hand while the service is registered, stop it
  first. It reads the saved settings first and restarts nothing when they cannot
  be used (exit 3 and the message above; exit 1 for a row from a newer version):
  a daemon that cannot use them starts without the OpenAI-compatible API, and
  says why on stderr and in its log, while it runs everything else.
  --db-path on api config edits a database that the login service's daemon
  does not read.

  From MCP (monoagentcli mcp): api_status, api_config_get, api_config_set and
  api_config_apply return the documents of api status --json, api config show
  --json, api config set|unset --json and daemon restart --json, with
  "environment":"mcp" where the command says "shell". api_config_set refuses a
  change that reaches further, and saves nothing, unless the operator started
  mcp with --allow-api-exposure (or MONOAGENT_MCP_ALLOW_API_EXPOSURE=1): a
  switch of the host's own configuration that no argument of a tool can set,
  since the model sets the arguments. What reaches further is what needs --yes
  above: a dedicated listener that reaches further than the saved one (beyond
  this machine, another host beyond it, or every interface where it was one
  host), a higher confinement class, a runtime list that gains a runtime it did
  not have, none left (tool calling or image generation switched on again), and
  removing a saved row that cannot be read (saved_settings). It also refuses a
  value that holds an API key, and no error of it repeats an argument. The flag
  guards that tool and api_auto_set only: --allow-mutations also serves
  workflow_node_add (which accepts the node type system.execute_command),
  workflow_set_active and workflow_run, so a model that has them can have a
  workflow of the profile run api config set ... --yes as the OS user; if a model
  must not be able to widen the server, do not give it --allow-mutations, or
  start mcp with --api-only (or MONOAGENT_MCP_API_ONLY=1), which serves the API's
  tools (api_*) and no other: no workflow, vault, secret, person, org or
  documentation tool. The mutating API tools still need --allow-mutations then,
  and --allow-api-exposure is still what lets a model widen the server or switch
  the auto model on. api_config_apply restarts the
  daemon as daemon restart does (it reads the saved settings first and restarts
  nothing when they cannot be used), and interrupts what it is running.
  api_auto_set switches the api_auto surface of the call's profile on (it needs
  --allow-api-exposure on the server, since what leaves the machine is the
  operator's decision, and acknowledge_egress: true, because prompts then go to
  TypeSafe) or off (it needs neither), as jev enable|disable api_auto does, and
  adds auto, what api_status says of the auto model; it never creates or reads
  the Jev key. api_models_list reads the saved settings under its server's
  environment, as api models does.

  Walkthrough: examples/openai-api-quickstart.md. Security model: SECURITY.md.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
OUTPUT REDACTION
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Output items go through the same redaction as workflow run --json
  (credential-shaped keys masked, values truncated at 4KB). Pass header
  "X-Full-Outputs: 1" to opt out per request, like the user's own
  workflow run --full-outputs. Unlike that flag, the header has no
  operator check: whoever holds the bearer credential gets unredacted
  outputs, so give the token only to what may see your secrets.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
STATUS-CODE MAPPING (mirrors CLI exit codes — see "ref commands")
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  HTTP status   Exit code   Cause
  200           0           success
  400           3           invalid input / validation failure (bad JSON
                             body, ErrNoTriggerNode, ErrCycleDetected,
                             ErrNodeTypeUnknown, ErrInvalidConfig,
                             ErrWorkflowInactive)
  401           4           missing or invalid bearer credential
  404           2           workflow, node type, or HIL item not found
                             (includes cross-profile lookups)
  500           1           unclassified engine/store error

  No endpoint ever returns a generic 500 for a condition the CLI
  classifies more specifically — this repo prefers honest, mapped
  statuses over collapsing everything to 200/500.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
EXAMPLE
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  monoagentcli httpapi &
  monoagentcli --json --profile <profile> secret reveal httpapi-token --reveal   # the user, in their terminal: copy "token"
  curl -s http://127.0.0.1:9322/health
  curl -s -H 'Authorization: Bearer <paste credential here>' \
    http://127.0.0.1:9322/profiles/<profile>/workflows

`)
		},
	}
}
