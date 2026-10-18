package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/openaiapi"
)

// apiTools are the tools over the API keys of the OpenAI-compatible API (/v1) and
// the models it would serve (spec §8.3). api_key_list lists the call's profile's
// keys; api_key_create, api_key_update and api_key_revoke change none, since only
// the user does that. What api_models_list computes is internal/openaiapi's, the
// same code `monoagentcli api` runs.
func apiTools() []tool {
	return []tool{
		{
			name:        "api_key_list",
			description: "List the call's profile's API keys for the OpenAI-compatible API (/v1): metadata only, never a key, since only its SHA-256 is stored. Revoked keys are included only with include_revoked.",
			schema: objSchema(map[string]interface{}{
				"include_revoked": boolParam("Also list revoked keys (default false)"),
			}),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolAPIKeyList,
		},
		{
			name:        "api_models_list",
			description: "List the models the OpenAI-compatible API (/v1) would serve, with each one's confinement class and capabilities: the document of `monoagentcli api models --json`. The first call takes a few seconds while the installed runtimes are asked for their lists. " + apiToolsDetails,
			schema: objSchema(map[string]interface{}{
				"for":                 strParam("loopback (default) or network: the kind of listener to evaluate"),
				"confinement":         strParam("Strongest class the listener serves: chat-only, sandboxed or any (default: the environment, else the saved confinement, else any on loopback and chat-only on a network listener)"),
				"context_confinement": strParam("Strongest class a key created with context may use: chat-only, sandboxed or any (default: the environment, else the saved context_confinement, else chat-only; never above the listener's)"),
				"auto_confinement":    strParam("Strongest class the auto model may pick: chat-only, sandboxed or any (default: the environment, else the saved auto_confinement, else chat-only; never above the listener's)"),
			}),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolAPIModelsList,
		},
		{
			name:        "api_key_create",
			description: "Creates no key, for any caller: an API key for the OpenAI-compatible API (/v1) signs in as the profile, so only the user creates one, with `monoagentcli --profile <id> api key create --name <name>` in their own terminal, or in the app. A call is refused and changes nothing. " + apiToolsDetails,
			schema: objSchema(map[string]interface{}{
				"name":    strParam("Key name, unique among the profile's active keys"),
				"context": boolParam("Add the profile's own knowledge (documents and captures) to requests made with this key (default false)"),
			}, "name"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     toolAPIKeyCreate,
		},
		{
			name: "api_key_update",
			description: "Changes no key, for any caller: renaming an API key of the OpenAI-compatible API (/v1) or switching its knowledge context changes what a key that signs in as the profile reaches, " +
				"so only the user does it, with `monoagentcli --profile <id> api key update <id|name> [--name N] [--context | --no-context]` in their own terminal, or in the app. A call is refused and changes nothing.",
			schema: objSchema(map[string]interface{}{
				"id":      strParam("The key's id (key_…) or the name of an active key"),
				"name":    strParam("New name (omit to leave unchanged)"),
				"context": boolParam("true adds the profile's own knowledge to requests made with the key, false stops it (omit to leave unchanged)"),
			}, "id"),
			annotations: map[string]bool{"readOnlyHint": false},
			mutating:    true,
			handler:     toolAPIKeyUpdate,
		},
		{
			name: "api_key_revoke",
			description: "Revokes no key, for any caller: revoking an API key of the OpenAI-compatible API (/v1) cuts off whatever signs in with it, " +
				"so only the user does it, with `monoagentcli --profile <id> api key revoke <id|name>` in their own terminal, or in the app. A call is refused and changes nothing.",
			schema: objSchema(map[string]interface{}{
				"id": strParam("The key's id (key_…) or the name of an active key"),
			}, "id"),
			annotations: map[string]bool{"readOnlyHint": false, "destructiveHint": true},
			mutating:    true,
			handler:     toolAPIKeyRevoke,
		},
	}
}

// apiKeys opens the key store and the profile of the call.
func (s *Server) apiKeys(ctx context.Context) (store *apikeys.Store, profileID string, err error) {
	rt, err := s.runtime()
	if err != nil {
		return nil, "", err
	}
	return apikeys.NewStore(rt.db.DB), profileOf(ctx), nil
}

func toolAPIKeyList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		IncludeRevoked bool `json:"include_revoked"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	store, profileID, err := s.apiKeys(ctx)
	if err != nil {
		return nil, err
	}
	return store.List(ctx, profileID, a.IncludeRevoked)
}

// The tools below change no key. A key signs in as the profile, and whoever
// calls an MCP tool is an AI, so only the user makes one, changes what it
// reaches or revokes it, as `api key create|update|revoke` refuse an agent,
// the chat and a workflow. The tools stay listed so that a call is told so.

func toolAPIKeyCreate(ctx context.Context, _ *Server, _ json.RawMessage) (interface{}, error) {
	return nil, keyRefusal(ctx, "api_key_create makes no key", "creates an API key, which signs in as this profile", "create --name <name>")
}

func toolAPIKeyUpdate(ctx context.Context, _ *Server, _ json.RawMessage) (interface{}, error) {
	return nil, keyRefusal(ctx, "api_key_update changes no key", "changes what an API key reaches", "update <id|name>")
}

func toolAPIKeyRevoke(ctx context.Context, _ *Server, _ json.RawMessage) (interface{}, error) {
	return nil, keyRefusal(ctx, "api_key_revoke revokes no key", "revokes an API key, which cuts off whatever signs in with it", "revoke <id|name>")
}

// keyRefusal is the answer of those tools: what the user runs instead.
func keyRefusal(ctx context.Context, refused, only, command string) error {
	return fmt.Errorf("%s: only the user %s: `monoagentcli --profile %s api key %s` in their own terminal, or the app", refused, only, profileOf(ctx), command)
}

// modelsTTL is how long api_models_list reuses the list of models it loaded. After
// it the previous list is served at once while a new one is loaded in the
// background, as the gateway's own catalog does.
const modelsTTL = time.Minute

// modelCatalog is the one catalog of models of this server. Calls at once share a
// load, and a list is reused for modelsTTL, so that a host that asks again does not
// start every installed runtime each time (a catalog made per call did: twenty
// calls started a hundred processes).
func (rt *runtime) modelCatalog() *openaiapi.Catalog {
	rt.catalogOnce.Do(func() { rt.catalog = openaiapi.NewModelCatalog(rt.db.DB, modelsTTL) })
	return rt.catalog
}

// maxEnumArgLen is far above the longest value any argument of api_models_list takes
// ("sandboxed", "loopback"): a longer one is refused before it is looked at.
const maxEnumArgLen = 32

// errBadClass is the refusal of a confinement class. It names the environment
// variable too, because that is what gives the value when the argument is left out.
func errBadClass(arg, env string) error {
	return fmt.Errorf("%s (or %s, when it is left out) must be chat-only, sandboxed or any", arg, env)
}

func toolAPIModelsList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		For                string `json:"for"`
		Confinement        string `json:"confinement"`
		ContextConfinement string `json:"context_confinement"`
		AutoConfinement    string `json:"auto_confinement"`
	}
	if err := decodeArgs(args, &a); err != nil {
		return nil, err
	}
	// The refusals below are fixed texts, and a value is looked at only after its
	// length: a caller may have put anything, a key included, into an argument, and
	// what it sent does not come back. The shared parsers say more (they quote the
	// value, which suits a command line) and are left as they are.
	for _, arg := range []struct{ name, value string }{
		{"for", a.For}, {"confinement", a.Confinement}, {"context_confinement", a.ContextConfinement}, {"auto_confinement", a.AutoConfinement},
	} {
		if len(arg.value) > maxEnumArgLen {
			return nil, fmt.Errorf("%s is too long", arg.name)
		}
	}
	if a.For == "" {
		a.For = "loopback"
	}
	addr, err := openaiapi.ListenerAddr(a.For)
	if err != nil {
		return nil, errors.New("for must be loopback or network")
	}
	rt, err := s.runtime()
	if err != nil {
		return nil, err
	}
	// What a server started now would read: this server's environment, with the settings saved
	// with api_config_set under it. A saved setting that fails its rule stops such a server, so it
	// stops this call too, naming the setting and not repeating its value.
	getenv, err := apiconfig.EnvWithSaved(ctx, rt.db.DB, s.getenv())
	if err != nil {
		return nil, err
	}
	policy, err := openaiapi.EffectivePolicy(addr, a.Confinement, getenv)
	if err != nil {
		return nil, errBadClass("confinement", "MONOAGENT_API_CONFINEMENT")
	}
	if policy.ContextMax, err = openaiapi.EffectiveContextMax(a.ContextConfinement, getenv); err != nil {
		return nil, errBadClass("context_confinement", "MONOAGENT_API_CONTEXT_CONFINEMENT")
	}
	if policy.AutoMax, err = openaiapi.EffectiveAutoMax(a.AutoConfinement, getenv); err != nil {
		return nil, errBadClass("auto_confinement", "MONOAGENT_API_AUTO_CONFINEMENT")
	}
	imageRuntimes, err := openaiapi.EffectiveImageRuntimes(getenv)
	if err != nil { // a fixed text, like the others: the shared parser quotes the value
		return nil, errors.New("MONOAGENT_API_IMAGE_RUNTIMES must be a comma-separated list of runtime ids, such as codex,antigravity")
	}
	toolRuntimes, err := openaiapi.EffectiveToolRuntimes(getenv)
	if err != nil { // a fixed text too
		return nil, errors.New("MONOAGENT_API_TOOL_RUNTIMES must be a comma-separated list of runtime ids, such as claude,codex")
	}
	// Bound to the call's context: it ends when the server stops its calls, and the
	// call does not wait for monomind's processes to close their pipes.
	models, err := rt.modelCatalog().ModelsBound(ctx)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	return openaiapi.NewModelsReport(openaiapi.ModelsReportInput{
		For: a.For, Policy: policy, Source: openaiapi.ReportSourceMCP, Models: models, ImageRuntimes: imageRuntimes, ToolRuntimes: toolRuntimes,
		Auto: openaiapi.DefaultAuto(rt.db.DB).Status(ctx, profileOf(ctx)),
	}), nil
}
