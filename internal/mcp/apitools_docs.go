package mcp

import "github.com/monoes/mono-agent/internal/apikeys"

// apiToolsTopic is the docs topic (and monoagent://ref/ resource) that holds
// the full rules of the api_* tools, whose descriptions in tools/list are
// kept to a few sentences.
const apiToolsTopic = "api-tools"

// apiToolsDetails ends the short description of an api_* tool that has more
// to say: where the rest is.
const apiToolsDetails = "Full rules: docs topic or resource monoagent://ref/api-tools."

// apiToolDetails is, by tool, what the description of each api_* tool said
// in full before it was shortened. Nothing in it was dropped: it is served
// as the api-tools topic (see init), and the tests that pin what a model is
// told of a tool read it here.
var apiToolDetails = map[string]string{
	"api_status": "Show where the OpenAI-compatible API (/v1) listens and whether it answers: the document of `monoagentcli api status --json`. " +
		"It has the call's profile's count of active API keys, whether a daemon is running and the addresses it reports, whether the auto model works for the profile and what it is missing, " +
		"and each listener (the main HTTP API listener, which serves /v1 only while it is bound to loopback, and the dedicated v1_addr listener): its address, whether it is bound to loopback, " +
		"the confinement class it serves and the classes a key created with context and the auto model are held to on it, where that came from (confinement_source is daemon when the running daemon reported it, " +
		"and environment when it is worked out from this MCP server's environment, the settings saved with api_config_set and the defaults, which a running server started with flags of its own may not share), " +
		"the scheme that answered (http or https), whether GET /health answers and whether /v1 does. " +
		"It probes each listener over HTTP, which takes a few seconds when nothing answers. It changes nothing and returns no key: " +
		"api_config_get says, setting by setting, what is saved and what the running daemon started with. " + damagedRowNote,
	"api_config_get": "Show the settings of the OpenAI-compatible API's server and where each stands: the document of `monoagentcli api config show --json`. " +
		"There are ten: v1_addr (the dedicated /v1 listener), tls_cert_file and tls_key_file (paths, never contents), confinement, context_confinement and auto_confinement " +
		"(the strongest runtime class the server serves, a key created with context may use and the auto model may pick), max_concurrent, turn_timeout, image_runtimes and tool_runtimes. " +
		"For each: what is saved (api_config_set saves it), what a server started from this MCP server's environment would use and where that comes from (env, saved or default: flag, then environment variable, then saved, then default), " +
		"and, when a daemon is running and reports it, the value the daemon started with and where that came from (flag, env, saved or default), with a state: " +
		"applied; pending_restart (saved since the daemon started: api_config_apply restarts it); overridden (the daemon was given a flag or a variable of its own, so a saved value has no effect until that is removed); " +
		"not_serving (only v1_addr and the TLS files: the daemon took the value, but its dedicated listener is not up, because it could not bind the address or load the certificate: its log says which, and a restart does not cure it until the setting is corrected); " +
		"not_running; or unknown (a daemon that predates the report). Also daemon.autostart (whether api_config_apply can restart the daemon), restart_needed, " +
		"and problems (a saved value that fails its rule, which api_config_set can replace or remove). It changes nothing. " + damagedRowNote,
	"api_config_set": "Save settings of the OpenAI-compatible API's server, or remove saved ones, in the database: the document of `monoagentcli api config set|unset --json`, which is api_config_get's document for the state after the change " +
		"plus applied, changed (the keys whose saved value is different now) and widening ([{key, reason}]). " +
		"Give set, an object of setting keys to values in the syntax of the setting's environment variable (the keys and what each accepts are in the schema), and/or unset, a list of keys or the single word all. " +
		"Only what is given changes. The checks are the command's, the same code: a value that fails its rule is refused naming the setting and the rule, an empty value is refused (use unset), " +
		"tls_cert_file and tls_key_file must end up together and be absolute paths (the daemon starts in another folder), no value may contain a control character, and nothing is saved from a call that is refused in any part. A value that holds an API key (sk-ma-) is refused: nothing here takes one, and api_config_get shows what is saved. " +
		"A saved setting takes effect when the server starts, never while it runs: this tool restarts nothing (api_config_apply does, which interrupts what the daemon is running), " +
		"and a flag or variable the daemon was given overrides a saved value (api_config_get says which). " +
		"A change that makes the server reach further than it did is refused, and nothing is saved, unless the operator started this MCP server with --allow-api-exposure: " +
		"a dedicated listener beyond this machine, or moved to another host beyond it or to every interface (an empty host, 0.0.0.0 or [::]), " +
		"a higher confinement class (of a listener, of a key created with context or of the auto model), " +
		"a runtime list that gains a runtime it did not have (one of the default list that a saved list left out counts when it comes back), tool calling or image generation switched on again, which includes removing a confinement of chat-only or an image_runtimes of none or of codex, " +
		"and removing saved settings that cannot be read (unset all), since what they limited cannot be told. " +
		"No argument can allow it, because the model sets the arguments and only the operator sets that flag. When it refuses it says which setting and why, without any address (api_config_get shows what is saved), " +
		"and that the user can make the change with `monoagentcli api config set ... --yes` (or unset) or in the desktop app. " +
		"When the operator allowed it, the reasons are in widening. Narrowing, max_concurrent, turn_timeout and the TLS files never need it. " +
		"While the saved settings are damaged (a row that cannot be read) every other change fails with a message that starts `the saved settings are damaged`: tell the user, " +
		"who can run `monoagentcli api config unset --all --yes`, or allow this call to remove the row (unset all, with --allow-api-exposure), and the result then says removed_unreadable_row; " +
		"a row that a newer version saved fails too, and nothing here removes it. Two calls at once both land.",
	"api_config_apply": "Restart the daemon so that it reads the settings saved with api_config_set: what `monoagentcli daemon restart` does, through the same function and the service manager the daemon is registered with " +
		"(a LaunchAgent on macOS, a systemd --user unit on Linux, a Scheduled Task on Windows; `monoagentcli daemon install` registers it). " +
		"The result is the document of `daemon restart --json`: {restarted, via}, via being launchd, systemd or schtasks. " +
		"It interrupts whatever the daemon is running (workflows, org runs). How the daemon ends is the service manager's, and nothing here promises that it finishes what it is doing first. " +
		"Use it when api_config_get says restart_needed (some setting is pending_restart) and the user is ready for that. It saves no setting and takes no argument. " +
		"It reads the saved settings first and restarts nothing when they cannot be used (a saved value that fails its rule, or damaged settings), because a daemon that cannot use them starts without the API: it fails with the message api_config_get gives. " +
		"A daemon that is not registered for auto-start (daemon.autostart is false in api_config_get) is not restarted by anything: the call fails and says that the user can stop it and start `monoagentcli daemon` again, " +
		"or run `monoagentcli daemon install` to have the system manage it. A daemon started by hand while the service is registered has to be stopped first. " +
		"Afterwards api_config_get shows what the daemon started with.",
	"api_auto_set": "Switch the auto model of the OpenAI-compatible API (the Jev surface api_auto) on or off for the call's profile, as `monoagentcli jev enable|disable api_auto` does: " +
		"the document of that command's --json (profile_id, surface, enabled and, when switching on, threshold and egress) plus auto, what api_status says of the auto model after the change " +
		"(available, or what it is missing: the surface, a Jev key). enabled is required. " +
		"Switching on needs two things, because while it is on the first 4,000 characters of the last user message of each request for the model auto (of an image request, its prompt) " +
		"and the names, descriptions and validated cost and latency of the models the server serves go to TypeSafe: the operator must have started this MCP server with --allow-api-exposure " +
		"(or MONOAGENT_MCP_ALLOW_API_EXPOSURE=1), which no argument of this tool can set, since the model sets the arguments, and acknowledge_egress: true. Without the first the call is refused and nothing changes " +
		"(the user can run `monoagentcli jev enable api_auto` or use the desktop app, Settings › Jev); without the second it is refused, shows exactly what would be sent, and nothing changes. " +
		"acknowledge_egress is set by the caller: like --yes of the command it makes the data visible before it moves, and it is not an access control. Switching off needs neither. " +
		"This tool never creates, stores, uses or shows the Jev key (the user stores it with `monoagentcli jev key set` in their own terminal, or TYPESAFE_API_KEY is set where the server runs): " +
		"with no key the surface is on and auto stays unavailable, which auto says. " +
		"It changes nothing else: the profile's threshold, its other Jev surfaces and other profiles are untouched. Which models auto may pick is auto_confinement (api_config_set), not this.",
	"api_key_list": "List the API keys of the call's profile, for the OpenAI-compatible API (/v1): [{id, profile_id, name, prefix, context, created_at, last_used_at, revoked_at}]. Metadata only: a key itself is never part of it, because only its SHA-256 is stored and it was shown once, when it was created. Revoked keys are included only with include_revoked.",
	"api_key_create": "Creates no key, for any caller: an API key for the OpenAI-compatible API (/v1) authenticates /v1 requests as the profile, so only the user creates one, " +
		"with `monoagentcli --profile <id> api key create --name <name> [--context]` in their own terminal or in the app, which shows the key to the user once and puts it in no transcript " +
		"(the command refuses an agent's shell tool, the chat and a workflow). A call of this tool is refused and changes nothing; api_key_list then shows the key the user made, never the key itself. " +
		"What to tell a user who asks: the name rule is the store's: " + apikeys.ErrInvalidName.Error() + ". A name must also be unique among the profile's active keys. " +
		"With --context, requests made with the key get excerpts of the profile's own knowledge added, and such a key is served only by chat-only models, and is refused tool calling, unless the server raises --context-confinement (and --confinement, on a chat-only listener) above chat-only: " +
		"the settings context_confinement and confinement, which api_config_get shows (saved, and as the running daemon started with them) and api_config_set changes, a change that reaches further and that the operator must have allowed.",
	"api_models_list": "List the models the OpenAI-compatible API (/v1) would serve, with each one's confinement class (chat-only, sandboxed or unconfined), " +
		"its capabilities (text; image for a model of a runtime that makes images; tools for a model that serves tool calling: its runtime is in the list below, monomind can apply the sandbox every turn with tools requires, and for a runtime that is not chat-only run it read-only), " +
		"whether a listener of the given kind serves it, whether a key created with context may use it, whether the auto model may pick it, " +
		"and whether the auto model works for the call's profile: the document of `monoagentcli api models --json`. " +
		"The policy comes from the arguments, else from MONOAGENT_API_CONFINEMENT, MONOAGENT_API_CONTEXT_CONFINEMENT and MONOAGENT_API_AUTO_CONFINEMENT " +
		"in this MCP server's environment, else from the settings saved for the server (api_config_get shows them, api_config_set changes them), else the listener's defaults. " +
		"The runtimes that make images come from MONOAGENT_API_IMAGE_RUNTIMES in this environment, else the saved image_runtimes, else codex and antigravity, " +
		"and the runtimes that serve tool calling from MONOAGENT_API_TOOL_RUNTIMES, else the saved tool_runtimes, else claude and codex (none switches either off). " +
		"That is what a server started now would apply, so it can differ from what a running one applies, which is what it read when it started: api_status and api_config_get say what it runs. " +
		"Loading the models asks every installed agent runtime for its list, which takes a few seconds: calls at once share one load, the list is reused for a minute, " +
		"and after that the previous one is served at once while a new one is loaded in the background, as the server's own /v1/models does. " + damagedRowNote,
}

// apiToolDetailOrder is the order of the tools in the api-tools topic.
var apiToolDetailOrder = []string{"api_status", "api_config_get", "api_config_set", "api_config_apply", "api_auto_set", "api_key_list", "api_key_create", "api_models_list"}

func init() {
	text := "THE API TOOLS (api_*): the OpenAI-compatible API's keys, models, status and settings\n"
	for _, name := range apiToolDetailOrder {
		text += "\n" + name + "\n  " + apiToolDetails[name] + "\n"
	}
	docsTopics[apiToolsTopic] = text
}

// apiToolText is all a model can read of an api_* tool: its description,
// then its rules in the api-tools topic.
func apiToolText(t tool) string {
	return t.description + "\n" + apiToolDetails[t.name]
}
