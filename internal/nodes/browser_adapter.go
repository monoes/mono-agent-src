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
	logger := newNopLogger()
	if os.Getenv("MONOAGENT_DEBUG") != "" {
		logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	}
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
		profileID:   vault.ProfileIDFromContext(ctx),
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
	if storage.db != nil {
		executor.SetSecretLookup(ScopedSecretLookup(ctx, storage.db, storage.profileID, strings.ToLower(b.platform)))
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
	if storage.db != nil {
		flushHealth(storage.db)
	}
	if err != nil {
		return nil, fmt.Errorf("nodes: BrowserNode execute %s/%s: %w", b.platform, b.actionType, err)
	}

	// 7. Convert and normalize ExtractedItems to a single output item.
	// All extracted items (one per bot method step) are merged together so the
	// downstream node sees a single item with all fields — including both the
	// input data (sheet row, prompts) and the action results (image_count,
	// response_text, etc.).  This prevents "3 items for 3 steps" fan-out.
	var inputJSON map[string]interface{}
	if len(input.Items) > 0 {
		inputJSON = input.Items[0].JSON
	}

	// List actions (comments, posts, followers, search results, one profile
	// per target) produce records: emit one item per record. Before, they
	// were merged into a single item and only the last record survived.
	if result.ListOutput && len(result.ExtractedItems) > 0 {
		return []workflow.NodeOutput{
			{Handle: "main", Items: recordItems(result.ExtractedItems, b.platform)},
		}, nil
	}

	// A declarative package's action also reports what it declared in
	// outputs.success (variables it set); native built-ins get nil here.
	declared := declaredOutputs(executor, b.platform, b.actionType)

	if len(result.ExtractedItems) > 0 {
		merged := withDeclaredOutputs(mergeStepResults(inputJSON, result.ExtractedItems, b.platform), declared)
		return []workflow.NodeOutput{
			{Handle: "main", Items: []workflow.Item{workflow.NewItem(merged)}},
		}, nil
	}

	if declared != nil {
		return []workflow.NodeOutput{
			{Handle: "main", Items: []workflow.Item{workflow.NewItem(withDeclaredOutputs(inputJSON, declared))}},
		}, nil
	}

	// No extracted items (e.g. publish actions) — pass input items through.
	if len(input.Items) > 0 {
		return []workflow.NodeOutput{
			{Handle: "main", Items: input.Items},
		}, nil
	}

	return []workflow.NodeOutput{
		{Handle: "main", Items: []workflow.Item{}},
	}, nil
}

// identityKeys name who or what a record is about; their presence means the
// record's "text" is content, not a profile card. The content keys cover the
// results of write actions (a TikTok comment or engagement result carries
// videoURL and the text it posted; X/Hacker News replies carry postURL or
// itemID), whose text is never a name. Plain "url" is left out: a profile
// card may carry one.
var identityKeys = []string{"full_name", "name", "username", "author", "author_username",
	"author_name", "handle", "displayName", "display_name", "title", "author_url", "comment_id", "post_url",
	"videoURL", "video_url", "postURL", "postUrl", "itemID", "item_id", "commentID"}

func hasIdentityFields(raw map[string]interface{}) bool {
	for _, k := range identityKeys {
		if v, ok := raw[k]; ok && v != nil && v != "" {
			return true
		}
	}
	return false
}

// recordItems turns each extracted record into its own output item,
// normalised like a merged item and with step bookkeeping removed.
func recordItems(extracted []map[string]interface{}, platform string) []workflow.Item {
	items := make([]workflow.Item, 0, len(extracted))
	for _, raw := range extracted {
		if rec := OutputRecord(raw, platform); rec != nil {
			items = append(items, workflow.NewItem(rec))
		}
	}
	return items
}

// OutputRecord is the output item a browser node emits for one extracted
// record: normalised (NormalizeBrowserItem) and without step bookkeeping.
// It is nil for a record a step marked skipped.
func OutputRecord(raw map[string]interface{}, platform string) map[string]interface{} {
	if skipped, _ := raw["skipped"].(bool); skipped {
		return nil
	}
	rec := make(map[string]interface{}, len(raw)+4)
	for k, v := range NormalizeBrowserItem(raw, platform) {
		if !stepBookkeepingKeys[k] {
			rec[k] = v
		}
	}
	return rec
}

// mergeStepResults folds the input item and every step's extracted result
// into the single item a downstream node sees (rather than fanning out "3
// items for 3 steps"). Later steps win for any key they share.
//
// A step's own bookkeeping never becomes the node's answer. A no-op step —
// navigating to a session id that wasn't supplied, uploading a reference
// image that wasn't set — reports {"success":true,"skipped":true,"reason":…},
// and merging that verbatim is how a Gemini run that saved a 1MB image
// announced itself as "skipped":true,"reason":"empty session_id" next to
// "image_count":1. That is also what made the same node, on a run that
// genuinely did nothing, look like it had merely skipped a step.
func mergeStepResults(inputJSON map[string]interface{}, extracted []map[string]interface{}, platform string) map[string]interface{} {
	merged := make(map[string]interface{}, len(inputJSON)+8)
	for k, v := range inputJSON {
		merged[k] = v
	}
	for _, raw := range extracted {
		if skipped, _ := raw["skipped"].(bool); skipped {
			continue
		}
		for k, v := range NormalizeBrowserItem(raw, platform) {
			if stepBookkeepingKeys[k] {
				continue
			}
			// Don't overwrite an existing social profile platform with "gemini"
			if k == "platform" && strings.ToLower(platform) == "gemini" {
				if existingP, ok := inputJSON["platform"].(string); ok && existingP != "" {
					continue
				}
			}
			merged[k] = v
		}
	}
	return merged
}

// stepBookkeepingKeys are fields a bot step reports about *how* it worked —
// which selector matched, how many characters it typed, whether a response had
// rendered — rather than what the node produced. They belong in logs, and as a
// node's output they are noise at best: a Gemini run that saved an image
// presented "skipped":true and "reason":"empty session_id" alongside
// "image_count":1, because a no-op step's map was merged in wholesale.
//
// "success" is deliberately absent: it is bookkeeping too, but a workflow may
// already branch on it, so it keeps surfacing.
var stepBookkeepingKeys = map[string]bool{
	"selector": true,
	"method":   true,
	"typed":    true,
	"ready":    true,
	"skipped":  true,
	"reason":   true,
}

// NormalizeBrowserItem enriches a raw extracted item with structured fields.
//
// stepExtractMultiple produces items with generic keys: "text" (visible text of
// the DOM element) and "href" (the element's href attribute if present).
// This function maps those to the canonical people fields that people.save
// and SaveExtractedData expect. Exported so cmd/monoagentcli's CLI `run`
// path can apply the same normalization stepExtractMultiple's workflow-node
// counterpart (BrowserNode.Execute, above) already does — the CLI path
// previously passed raw text/href items straight to SaveExtractedData,
// which silently skipped every row for lacking a resolvable username.
func NormalizeBrowserItem(raw map[string]interface{}, platform string) map[string]interface{} {
	out := make(map[string]interface{}, len(raw)+4)
	for k, v := range raw {
		out[k] = v
	}

	// Canonical platform field (lowercase).
	if _, exists := out["platform"]; !exists {
		out["platform"] = strings.ToLower(platform)
	}

	// Map href → profile_url (and url for SaveExtractedData compatibility).
	if href, ok := raw["href"].(string); ok && href != "" {
		if _, exists := out["profile_url"]; !exists {
			out["profile_url"] = href
		}
		if _, exists := out["url"]; !exists {
			out["url"] = href
		}
	}

	// Split text into full_name and job_title.
	// LinkedIn result cards include noise lines before the actual job title
	// (e.g. "View X's profile", "• 2nd", "2nd degree connection").
	// Scan past those to find the real professional headline.
	// Only bare profile cards (a text blob and a link) get their text split
	// into name fields. A record that already says who it is about — a
	// comment's author, a post's username — keeps its text as text: a
	// comment body is not a person's name.
	if text, ok := raw["text"].(string); ok && text != "" && !hasIdentityFields(raw) {
		trimmedText := strings.TrimSpace(text)

		// Check if text is a single-line or bullet-separated LinkedIn card
		if strings.Contains(trimmedText, "•") {
			parts := strings.SplitN(trimmedText, "•", 2)
			rawName := strings.TrimSpace(parts[0])
			// Deduplicate repeated name words (e.g. "Ravinder Singh Ravinder Singh")
			words := strings.Fields(rawName)
			if len(words) >= 2 && len(words)%2 == 0 {
				half := len(words) / 2
				if strings.EqualFold(strings.Join(words[:half], " "), strings.Join(words[half:], " ")) {
					rawName = strings.Join(words[:half], " ")
				}
			}
			if _, exists := out["full_name"]; !exists && rawName != "" {
				out["full_name"] = rawName
			}
			if _, exists := out["name"]; !exists && rawName != "" {
				out["name"] = rawName
			}

			if len(parts) > 1 {
				afterBullet := strings.TrimSpace(parts[1])
				// Strip leading degree indicators e.g. "1st", "2nd", "3rd"
				for _, deg := range []string{"1st", "2nd", "3rd"} {
					if strings.HasPrefix(afterBullet, deg) {
						afterBullet = strings.TrimSpace(strings.TrimPrefix(afterBullet, deg))
						break
					}
				}

				// Find boundaries for headline (Connect, Follow, Message, Current:, Past:)
				headline := afterBullet
				cutKeywords := []string{"Connect", "Follow", "Message", "Current:", "Past:"}
				for _, kw := range cutKeywords {
					if idx := strings.Index(headline, kw); idx != -1 {
						headline = strings.TrimSpace(headline[:idx])
					}
				}
				// Also check if headline ends with a location like "Berlin..."
				lowerH := strings.ToLower(headline)
				if idx := strings.Index(lowerH, "berlin"); idx != -1 {
					headline = strings.TrimSpace(headline[:idx])
				}
				headline = strings.TrimSuffix(strings.TrimSpace(headline), "|")
				headline = strings.TrimSpace(headline)

				if _, exists := out["job_title"]; !exists && headline != "" {
					out["job_title"] = headline
				}
				if _, exists := out["headline"]; !exists && headline != "" {
					out["headline"] = headline
				}
			}
		}

		lines := strings.Split(trimmedText, "\n")
		if _, exists := out["full_name"]; !exists && len(lines) > 0 {
			out["full_name"] = strings.TrimSpace(lines[0])
		}
		if _, exists := out["name"]; !exists {
			if fn, ok := out["full_name"].(string); ok && fn != "" {
				out["name"] = fn
			}
		}
		if _, exists := out["job_title"]; !exists {
			name, _ := out["full_name"].(string)
			for _, line := range lines[1:] {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				// Skip accessibility/noise lines common in LinkedIn cards.
				lower := strings.ToLower(line)
				if strings.HasPrefix(lower, "view ") && strings.Contains(lower, "profile") {
					continue
				}
				if strings.HasPrefix(line, "• ") || line == "Connect" || line == "Follow" ||
					line == "Message" || line == "InMail" || strings.HasPrefix(line, "Visit my") {
					continue
				}
				if strings.Contains(lower, "degree connection") || strings.Contains(lower, "mutual connection") {
					continue
				}
				if name != "" && strings.Contains(line, name) {
					continue
				}
				out["job_title"] = line
				break
			}
		}
		if _, exists := out["headline"]; !exists {
			if jt, ok := out["job_title"].(string); ok && jt != "" {
				out["headline"] = jt
			}
		}
	}

	return out
}
