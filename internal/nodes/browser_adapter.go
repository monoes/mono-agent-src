package nodes

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/rs/zerolog"
)

// globalSessionProvider is the process-wide SessionProvider injected at startup.
var globalSessionProvider SessionProvider

// globalBotRegistry is the process-wide BotRegistry injected at startup.
var globalBotRegistry BotRegistry

// globalConfigMgr is the process-wide config manager for selector resolution.
var globalConfigMgr action.ConfigInterface

// globalCredentialStore allows BrowserNode to resolve credential_id → username.
var globalCredentialStore *connections.Store

// SessionProvider returns a browser page for a given platform and session username.
type SessionProvider interface {
	GetPage(ctx context.Context, platform string, username string) (browser.PageInterface, error)
}

// BotRegistry returns a BotAdapter for a given platform.
type BotRegistry interface {
	GetAdapter(platform string) (action.BotAdapter, bool)
}

// SetGlobalSessionProvider sets the session provider used by all BrowserNodes.
// Called once during engine startup.
func SetGlobalSessionProvider(sp SessionProvider) {
	globalSessionProvider = sp
}

// GlobalSessionProvider returns the provider set at startup, or nil when the
// process runs without a browser (tests, headless tools).
func GlobalSessionProvider() SessionProvider {
	return globalSessionProvider
}

// SetGlobalBotRegistry sets the bot registry used by all BrowserNodes.
// Called once during engine startup.
func SetGlobalBotRegistry(br BotRegistry) {
	globalBotRegistry = br
}

// SetGlobalConfigMgr sets the config manager used by all BrowserNodes for selector resolution.
func SetGlobalConfigMgr(cm action.ConfigInterface) {
	globalConfigMgr = cm
}

// SetGlobalCredentialStore sets the connections store used to resolve
// credential_id values to a platform username. Call once at startup.
func SetGlobalCredentialStore(cs *connections.Store) {
	globalCredentialStore = cs
}

// BrowserNode wraps the existing action.ActionExecutor to satisfy the workflow.NodeExecutor interface.
// This allows all existing platform actions (Instagram, LinkedIn, etc.) to be used as workflow nodes.
type BrowserNode struct {
	platform   string // "instagram", "linkedin", "tiktok", "x"
	actionType string // "BULK_FOLLOWING", "KEYWORD_SEARCH", etc.
}

// newNopLogger returns a zerolog.Logger that discards all output.
func newNopLogger() zerolog.Logger {
	return zerolog.New(io.Discard).With().Logger()
}

// actionLogger is the logger handed to the ActionExecutor: a discard logger,
// or with MONOAGENT_DEBUG set the shared root logger at every level (the
// variable has always meant "show all of it", whatever --log-level says).
func actionLogger() zerolog.Logger {
	if os.Getenv("MONOAGENT_DEBUG") != "" {
		return logging.Zerolog().Level(zerolog.TraceLevel)
	}
	return newNopLogger()
}

// NewBrowserNode creates a new BrowserNode for the given platform and action type.
func NewBrowserNode(platform, actionType string) *BrowserNode {
	return &BrowserNode{
		platform:   platform,
		actionType: actionType,
	}
}

// Type returns the node type string this node is actually registered under
// in the workflow registry: "{platform}.{actionType}", e.g.
// "instagram.KEYWORD_SEARCH" — see RegisterBrowserNodes in
// browser_register.go, which is the single source of truth for how these
// node types are keyed. (Previously returned "action.{platform}.{actionType}",
// which never matched the registered key — any caller trusting .Type() to
// round-trip through the registry would have silently failed.)
func (b *BrowserNode) Type() string {
	return fmt.Sprintf("%s.%s", b.platform, b.actionType)
}

// LiteralConfigFields: username picks the saved session (the account the
// node acts as), as credential_id picks a saved connection
// (workflow.LiteralConfigResolver).
func (b *BrowserNode) LiteralConfigFields() []string { return []string{"username"} }

// Execute runs the platform action against the bot's browser.
// It converts workflow Items to action parameters, runs ActionExecutor, and converts results back to Items.
//
// The SessionProvider and BotRegistry must be set via SetGlobalSessionProvider and SetGlobalBotRegistry
// before Execute is called.
func (b *BrowserNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if globalSessionProvider == nil {
		return nil, fmt.Errorf("nodes: SessionProvider not set; call SetGlobalSessionProvider at startup")
	}

	// 1. Extract username from config.
	// If credential_id is set, resolve it via the connections store to get the
	// stored username. Fall back to the explicit "username" field, then "unknown".
	username, _ := config["username"].(string)
	if credID, ok := config["credential_id"].(string); ok && credID != "" && globalCredentialStore != nil {
		if conn, err := globalCredentialStore.Get(ctx, credID); err == nil && conn != nil {
			if u, _ := conn.Data["username"].(string); u != "" {
				username = u
			} else if conn.AccountID != "" {
				username = conn.AccountID
			}
		}
	}
	if username == "" {
		username = "unknown"
	}

	// 2. Build a StorageAction from config fields.
	storageAction := &action.StorageAction{
		ID:             uuid.New().String(),
		Type:           b.actionType,
		TargetPlatform: b.platform,
	}

	if msg, ok := config["message"].(string); ok {
		storageAction.ContentMessage = msg
	}
	if kw, ok := config["keywords"].(string); ok {
		storageAction.Keywords = kw
	}

	// Collect selectedListItems from "targets" (or, when that is absent, a
	// config key named selectedListItems — which used to be silently ignored).
	var selectedListItems []interface{}
	targetsRaw, ok := config["targets"]
	if !ok {
		targetsRaw, ok = config["selectedListItems"]
	}
	if ok {
		if targets, ok := targetsRaw.([]interface{}); ok {
			for _, t := range targets {
				switch v := t.(type) {
				case string:
					selectedListItems = append(selectedListItems, map[string]interface{}{
						"url":      v,
						"href":     v,
						"username": v,
					})
				case map[string]interface{}:
					selectedListItems = append(selectedListItems, v)
				default:
					selectedListItems = append(selectedListItems, t)
				}
			}
		}
	}

	// Build a Params map from all remaining config keys (excluding reserved keys).
	reserved := map[string]struct{}{
		"username": {},
		"targets":  {},
		// selectedListItems is seeded from targets below, never as a param.
		"selectedListItems": {},
		"message":           {},
		"keywords":          {},
	}
	params := make(map[string]interface{})
	for k, v := range config {
		if _, skip := reserved[k]; !skip {
			params[k] = v
		}
	}
	// If maxResultsCount is not explicitly set but limit is provided, alias it
	// so social actions requiring maxResultsCount receive the limit field from node config.
	if _, ok := params["maxResultsCount"]; !ok {
		if lim, ok := params["limit"]; ok {
			params["maxResultsCount"] = lim
		}
	}
	// Seed session username so {{username}} resolves in actions that reference it.
	// If a "targetUsername" key is provided it overrides {{username}} in template context,
	// allowing callers to distinguish session identity from action target.
	// The "unknown" placeholder is never seeded: an action whose target input
	// falls back to username (e.g. list_user_posts' target_url) would
	// otherwise run against a profile called "unknown" instead of reporting
	// the target as missing.
	if username != "unknown" {
		params["username"] = username
	}
	if targetU, ok := config["targetUsername"].(string); ok && targetU != "" {
		params["username"] = targetU
	}
	storageAction.Params = params

	// 3. Check required inputs before a session exists. A node whose config
	// resolves to nothing — a {{ json $json.prompts }} that rendered "null",
	// say — used to open a browser tab, run zero loop iterations and still
	// report success. Failing first costs nothing and names what is missing.
	if err := action.ValidateActionInputs(b.platform, b.actionType, storageAction,
		// "targets" is the node-facing name of the same list: actions that
		// declare their required list input as "targets" (e.g.
		// linkedin.list_user_posts) must validate against it too.
		map[string]interface{}{"selectedListItems": selectedListItems, "targets": selectedListItems}); err != nil {
		return nil, fmt.Errorf("nodes: %s/%s: %w", b.platform, b.actionType, err)
	}
	if err := checkLiveRun(b.platform, b.actionType); err != nil {
		return nil, fmt.Errorf("nodes: %w", err)
	}

	// A retry runs only the targets an earlier attempt did not finish
	// (browser_adapter_items.go).
	var progress *targetProgress
	if len(selectedListItems) > 0 {
		progress = newTargetProgress(ctx, selectedListItems)
		if selectedListItems = progress.remaining(selectedListItems); len(selectedListItems) == 0 {
			progress.finish()
			return passThroughOutputs(input), nil
		}
	}

	// 4. Get a session (browser page) via the SessionProvider.
	// Each call opens a fresh tab (no reuse across nodes), so close it once
	// this node is done rather than leaving it open for the process lifetime.
	page, err := globalSessionProvider.GetPage(ctx, b.platform, username)
	if err != nil {
		return nil, fmt.Errorf("nodes: getting page for %s/%s: %w", b.platform, username, err)
	}
	defer page.Close() //nolint:errcheck

	// 5. Get the appropriate bot adapter via the BotRegistry (optional — not
	// all platforms need it): the package's requires.native bot, if any.
	var botAdapter action.BotAdapter
	if globalBotRegistry != nil {
		botAdapter, _ = globalBotRegistry.GetAdapter(nativePlatform(b.platform))
	}

	// 6. Create ActionExecutor and call Execute.
	logger := actionLogger()
	// db/profileID reach BrowserNode via context, set once by the workflow
	// engine (internal/workflow/engine.go) before any node executes — the
	// same context-based DI pattern org.run uses (internal/nodes/org/run.go).
	// Backing this with a real StorageInterface (rather than nil, as before)
	// is what makes running these node types inside a workflow persist
	// interaction history (workflow_node_targets) and honor daily rate caps
	// (workflow_daily_counters) — previously both silently no-op'd for any
	// node-graph run of these types, only working through the standalone
	// Actions page's now-removed execution path.
	storage := &workflowActionStorage{
		db:          vault.DBFromContext(ctx),
		profileID:   storageProfile(ctx),
		executionID: input.ExecutionID,
		nodeID:      input.NodeID,
		platform:    b.platform,
	}
	executor := action.NewActionExecutor(
		ctx,
		page,
		storage,
		globalConfigMgr,
		nil, // events: no external monitoring channel
		botAdapter,
		logger,
	)
	// Record each confirmed publishing step immediately: later waits or other
	// targets can fail, and merged output cannot preserve per-target text.
	sequence := 0
	publicationAttempt := uuid.NewString()
	if storage.db != nil {
		executor.SuccessfulStep = func(step action.StepDef, result *action.StepResult) {
			if step.Type != "call_bot_method" {
				return
			}
			data, ok := result.Data.(map[string]interface{})
			if !ok {
				return
			}
			method := step.MethodName
			if method == "" {
				method = step.Method
			}
			source := workflow.PublicationSource(ctx, input)
			entry := publication.BrowserPublication(b.platform, method, step.Args, data, source, username, sequence)
			if entry == nil {
				return
			}
			sequence++
			if entry.RemoteID == "" && entry.URL == "" {
				entry.IdempotencyKey += ":" + publicationAttempt
			}
			if b.platform == "tiktok" && method == "publish_uploaded_video" {
				if path, ok := config["media"].(string); ok && path != "" {
					entry.Media = []string{path}
				}
			}
			publication.Record(ctx, publication.NewStore(storage.db, storage.profileID), []publication.Entry{*entry}, func(msg string) {
				logger.Warn().Msg(msg)
				fmt.Fprintln(os.Stderr, "Warning:", msg)
			})
		}
	}
	attachPackage(executor, b.platform, storage.db)
	var refused secretRefusals
	if storage.db != nil {
		executor.SetSecretLookup(ScopedSecretLookup(ctx, storage.db, storage.profileID, strings.ToLower(b.platform), refused.note))
	}

	// Opt-in Jev element-picker fallback for steps that declare an intent
	// (`monoagentcli jev enable action_fallback`). Disabled, or no key ⇒
	// the executor behaves exactly as before.
	if db, pid := storage.db, storage.profileID; db != nil && jevconf.Enabled(db, pid, jevconf.ActionFallback) {
		if client, err := jevconf.NewClient(ctx, db, pid, "", "", jevconf.ActionFallback); err == nil {
			executor.SetJevPicker(client, jevconf.Threshold(db, pid, jevconf.ActionFallback, jevconf.DefaultThreshold[jevconf.ActionFallback]))
			// Social bots that embed bot.JevPicker get the same client; 0 ⇒ their
			// own default gate (0.6). GetAdapter builds a fresh bot per node run.
			if jb, ok := botAdapter.(interface{ SetJevPicker(*jev.Client, float64) }); ok {
				jb.SetJevPicker(client, 0)
			}
		} else {
			logger.Debug().Err(err).Msg("jev action fallback enabled but no client")
		}
	}

	// Seed selectedListItems as a variable so loops over target lists work.
	if len(selectedListItems) > 0 {
		executor.SetVariable("selectedListItems", selectedListItems)
		// Same list under its node-facing name, for actions that declare
		// their required list input as "targets" (runtime validation reads it).
		executor.SetVariable("targets", selectedListItems)
	}

	result, err := executor.Execute(storageAction)
	// A {{secret:}} lookup this run may not do read nothing: a failed step
	// says so, and a step that went on without it logs it.
	if why := refused.first(); why != nil {
		if err != nil {
			err = fmt.Errorf("%w; %v", err, why)
		} else {
			logger.Warn().Msg(why.Error())
			fmt.Fprintln(os.Stderr, "Warning:", why)
		}
	}
	succeeded := 0
	if progress != nil && result != nil {
		succeeded = progress.record(result)
	}
	if storage.db != nil {
		flushHealth(storage.db)
	}
	// A profile read is kept: the packages' scrape_profile_info actions only
	// return what they read (TikTok's also saves it itself), so without this
	// the people it names never got their photo, bio or extras. This runs
	// before the error return: an account that no longer exists fails the
	// action, and must not cost the profiles read before and after it.
	if b.actionType == "scrape_profile_info" && result != nil && !storage.saved {
		var profiles []map[string]interface{}
		for _, raw := range result.ExtractedItems {
			if rec := OutputRecord(raw, b.platform); rec != nil {
				profiles = append(profiles, rec)
			}
		}
		if err := storage.SaveExtractedData(storageAction.ID, profiles); err != nil {
			logger.Warn().Err(err).Msg("could not save the profiles read")
		}
	}

	if err != nil {
		if pe := b.partialFailure(input, executor, result, succeeded, err); pe != nil {
			return nil, pe
		}
		return nil, fmt.Errorf("nodes: BrowserNode execute %s/%s: %w", b.platform, b.actionType, err)
	}
	if progress != nil {
		progress.finish()
	}
	// 7. Convert the result to the node's outputs.
	outs, _ := b.outputs(input, executor, result)
	return outs, nil
}
