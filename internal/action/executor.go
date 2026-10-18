package action

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// Step & loop definitions (deserialized from action JSON)
// ---------------------------------------------------------------------------

// StepDef represents a single step in an action definition.
type StepDef struct {
	ID            string                 `json:"id"`
	Type          string                 `json:"type"`
	URL           string                 `json:"url,omitempty"`
	Selector      string                 `json:"selector,omitempty"`
	XPath         string                 `json:"xpath,omitempty"`
	ConfigKey     string                 `json:"configKey,omitempty"`
	Alternatives  []string               `json:"alternatives,omitempty"`
	ElementRef    string                 `json:"elementRef,omitempty"`
	Value         interface{}            `json:"value,omitempty"`
	Text          string                 `json:"text,omitempty"`
	Attribute     string                 `json:"attribute,omitempty"`
	Direction     string                 `json:"direction,omitempty"`
	Duration      interface{}            `json:"duration,omitempty"`
	Timeout       float64                `json:"timeout,omitempty"`
	HumanLike     bool                   `json:"humanLike,omitempty"`
	MethodName    string                 `json:"methodName,omitempty"`
	Method        string                 `json:"method,omitempty"`
	Args          []interface{}          `json:"args,omitempty"`
	Variable      string                 `json:"variable,omitempty"`
	VariableName  string                 `json:"variable_name,omitempty"`
	WaitFor       string                 `json:"waitFor,omitempty"`
	WaitAfter     string                 `json:"waitAfter,omitempty"`
	RaceSelectors map[string]string      `json:"raceSelectors,omitempty"`
	Condition     interface{}            `json:"condition,omitempty"`
	Then          []string               `json:"then,omitempty"`
	Else          []string               `json:"else,omitempty"`
	OnError       *ErrorHandlerDef       `json:"onError,omitempty"`
	OnSuccess     *SuccessAction         `json:"onSuccess,omitempty"`
	Set           map[string]interface{} `json:"set,omitempty"`
	Description   string                 `json:"description,omitempty"`
	DataSource    string                 `json:"dataSource,omitempty"`
	BatchSize     int                    `json:"batchSize,omitempty"`
	Increment     string                 `json:"increment,omitempty"`
	// Intent describes the step's target element in plain words (e.g. "the
	// chat's Send button"). It is only used by the opt-in Jev fallback
	// (jevfallback.go) when the step's selector/xpath finds nothing; a step
	// without an intent never falls back.
	Intent string `json:"intent,omitempty"`

	// --- package-era fields (spec §4.3, §6.1) ---

	// SideEffect marks a step that writes, sends or deletes on the site.
	// Safe-mode verification (SetSafeMode) stops before the first one.
	SideEffect bool `json:"sideEffect,omitempty"`
	// Until is a post-step outcome to wait for (click/type/submit), and
	// the condition of a wait_for step.
	Until *WaitSpec `json:"until,omitempty"`
	// Fragment names fragments/<name>.json for call_fragment.
	Fragment string `json:"fragment,omitempty"`
	// Action is the call_action reference ("<action>" or "<automation>.<action>").
	Action string `json:"action,omitempty"`
	// Inputs are the variables passed to call_fragment / call_action /
	// page_script (values are templates).
	Inputs map[string]interface{} `json:"inputs,omitempty"`
	// Items is the for_each list template; As names the item variable
	// (default "item"); Steps is the inline loop body.
	Items string    `json:"items,omitempty"`
	As    string    `json:"as,omitempty"`
	Steps []StepDef `json:"steps,omitempty"`
	// Key is the key for press_key ("Enter", "Escape", "Control+a", ...).
	Key string `json:"key,omitempty"`
	// Script names scripts/<name>.js for page_script.
	Script string `json:"script,omitempty"`
	// Input is the source template for transform; Ops its operations.
	Input string        `json:"input,omitempty"`
	Ops   []TransformOp `json:"ops,omitempty"`
	// Fields maps output field → sub-selector for extract_table /
	// extract_list-style steps (relative to each row/item).
	Fields map[string]string `json:"fields,omitempty"`
	// Path is a JSON path ("a.b[0].c") for extract_json.
	Path string `json:"path,omitempty"`
}

// LoopDef defines an iteration over a collection of items, executing a subset
// of the action's steps for each item.
//
// NOTE: a legacy "onComplete" field appears in some shipped loop JSONs. It is
// deliberately NOT modeled here — it was historically parsed but never
// executed, so json.Unmarshal now silently ignores it for JSON compatibility.
type LoopDef struct {
	ID       string   `json:"id"`
	Iterator string   `json:"iterator"`
	IndexVar string   `json:"indexVar"`
	Steps    []string `json:"steps"`
	MaxItems string   `json:"maxItems,omitempty"`
	// MaxItemsPerDay is an optional template (e.g. "{{maxFollowsPerDay or
	// 150}}") resolving to a cross-run daily cap, persisted in the
	// action_daily_counters table and keyed by (profile, action type, UTC
	// day). Unlike MaxItems (a per-session, in-memory cap), this cap
	// survives across separate action runs on the same day.
	MaxItemsPerDay string `json:"maxItemsPerDay,omitempty"`
}

// ConditionDef describes a conditional expression for condition steps.
type ConditionDef struct {
	Variable string      `json:"variable"`
	Operator string      `json:"operator"` // "exists", "not_exists", "equals", "not_equals", "greater_than", "contains"
	Value    interface{} `json:"value,omitempty"`
}

// SuccessAction defines what happens when a step succeeds.
type SuccessAction struct {
	Action    string      `json:"action"` // "set_variable", "increment", "save_data", "update_progress"
	Variable  string      `json:"variable,omitempty"`
	Value     interface{} `json:"value,omitempty"`
	Increment string      `json:"increment,omitempty"`
}

// ---------------------------------------------------------------------------
// Event types
// ---------------------------------------------------------------------------

// ExecutionEvent is emitted during execution to allow external monitoring.
type ExecutionEvent struct {
	Type     string `json:"type"`
	ActionID string `json:"actionId"`
	StepID   string `json:"stepId,omitempty"`
	Index    int    `json:"index,omitempty"`
	Total    int    `json:"total,omitempty"`
	Message  string `json:"message,omitempty"`
}

// ---------------------------------------------------------------------------
// Execution context
// ---------------------------------------------------------------------------

// ExecutionContext holds all mutable state for a single action execution run.
type ExecutionContext struct {
	mu              sync.Mutex
	Variables       map[string]interface{}
	StepResults     map[string]*StepResult
	Elements        map[string]browser.ElementHandle
	Data            map[string]interface{}
	ExtractedItems  []map[string]interface{}
	FailedItems     []FailedItem
	RecursionCounts map[string]int
	CurrentURL      string
	// ListOutput is set when the action produced records — rows from
	// extract_multiple, a list-returning bot method, or one result per loop
	// iteration — as opposed to per-step results about a single item (the
	// Gemini shape). The node layer emits records as separate items.
	ListOutput bool
	loopDepth  int
}

// NewExecutionContext returns an initialised execution context.
func NewExecutionContext() *ExecutionContext {
	return &ExecutionContext{
		Variables:       make(map[string]interface{}),
		StepResults:     make(map[string]*StepResult),
		Elements:        make(map[string]browser.ElementHandle),
		Data:            make(map[string]interface{}),
		ExtractedItems:  make([]map[string]interface{}, 0),
		FailedItems:     make([]FailedItem, 0),
		RecursionCounts: make(map[string]int),
	}
}

// SetVariable stores a variable in the context.
func (ec *ExecutionContext) SetVariable(name string, value interface{}) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.Variables[name] = value
}

// GetVariable retrieves a variable by name.
func (ec *ExecutionContext) GetVariable(name string) (interface{}, bool) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	v, ok := ec.Variables[name]
	return v, ok
}

// SetStepResult records the result of a step execution.
func (ec *ExecutionContext) SetStepResult(stepID string, result *StepResult) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.StepResults[stepID] = result
}

// GetStepResult retrieves the result of a previously executed step.
func (ec *ExecutionContext) GetStepResult(stepID string) *StepResult {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	return ec.StepResults[stepID]
}

// SetElement stores an ElementHandle reference by name.
func (ec *ExecutionContext) SetElement(name string, elem browser.ElementHandle) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.Elements[name] = elem
}

// GetElement retrieves a previously stored ElementHandle reference.
func (ec *ExecutionContext) GetElement(name string) browser.ElementHandle {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	return ec.Elements[name]
}

// SetData stores an arbitrary value in the data map.
func (ec *ExecutionContext) SetData(key string, value interface{}) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.Data[key] = value
}

// GetData retrieves a value from the data map.
func (ec *ExecutionContext) GetData(key string) (interface{}, bool) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	v, ok := ec.Data[key]
	return v, ok
}

// AddFailedItem appends a failure record.
func (ec *ExecutionContext) AddFailedItem(item FailedItem) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.FailedItems = append(ec.FailedItems, item)
}

// HasFailedItems reports whether any step recorded a failure via
// AddFailedItem (the "mark_failed" onError action, and its onFailure
// fallback) during this run.
func (ec *ExecutionContext) HasFailedItems() bool {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	return len(ec.FailedItems) > 0
}

// AddExtractedItem appends an extracted data record.
func (ec *ExecutionContext) AddExtractedItem(item map[string]interface{}) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.ExtractedItems = append(ec.ExtractedItems, item)
}

// AddRecord appends an item that is one record of a list result (see
// ListOutput).
func (ec *ExecutionContext) AddRecord(item map[string]interface{}) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.ExtractedItems = append(ec.ExtractedItems, item)
	ec.ListOutput = true
}

// inLoop reports whether a loop body is currently executing.
func (ec *ExecutionContext) inLoop() bool {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	return ec.loopDepth > 0
}

func (ec *ExecutionContext) enterLoop() {
	ec.mu.Lock()
	ec.loopDepth++
	ec.mu.Unlock()
}

func (ec *ExecutionContext) leaveLoop() {
	ec.mu.Lock()
	ec.loopDepth--
	ec.mu.Unlock()
}

// IncrementRecursion increments and returns the recursion counter for a key.
func (ec *ExecutionContext) IncrementRecursion(key string) int {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	ec.RecursionCounts[key]++
	return ec.RecursionCounts[key]
}

// ---------------------------------------------------------------------------
// Result types
// ---------------------------------------------------------------------------

// FailedItem records a step failure for later reporting.
type FailedItem struct {
	StepID    string
	Error     error
	Timestamp time.Time
	Index     int
}

// StepResult captures the outcome of a single step execution.
type StepResult struct {
	Success bool
	Element browser.ElementHandle
	Data    interface{}
	Error   error
	Abort   bool
	Skip    bool
	Retry   bool
	StepID  string
}

// ExecutionResult is the aggregate outcome of an entire action execution.
type ExecutionResult struct {
	ExtractedItems []map[string]interface{}
	// ListOutput: ExtractedItems are separate records (see
	// ExecutionContext.ListOutput), not facets of one item.
	ListOutput     bool
	FailedItems    []FailedItem
	TotalProcessed int
	Duration       time.Duration
}

// ---------------------------------------------------------------------------
// Interfaces (to avoid circular imports with storage/config packages)
// ---------------------------------------------------------------------------

// StorageInterface abstracts the database operations needed by the executor.
type StorageInterface interface {
	UpdateActionState(id, state string) error
	UpdateActionReachedIndex(id string, index int) error
	SaveExtractedData(actionID string, items []map[string]interface{}) error
	// GetDailyActionCount returns how many times actionType has already
	// been recorded today (UTC, see action_daily_counters) for whatever
	// profile this StorageInterface implementation is scoped to.
	GetDailyActionCount(actionType string) (int, error)
	// IncrementDailyActionCount records one more occurrence of actionType
	// today (UTC) and returns the count after incrementing.
	IncrementDailyActionCount(actionType string) (int, error)
}

// ConfigInterface abstracts the configuration manager.
type ConfigInterface interface {
	GetConfig(social, action, configContext, htmlContent, purpose string, schema map[string]interface{}) (interface{}, error)
}

// BotAdapter is the interface for platform-specific bot methods that can be
// called via the "call_bot_method" step type.
type BotAdapter interface {
	// GetMethodByName returns a callable function for the given method name.
	// The returned function accepts a context and variadic args, returning
	// (result interface{}, err error).
	GetMethodByName(name string) (func(ctx context.Context, args ...interface{}) (interface{}, error), bool)
}

// StorageAction is a flat representation of an action from the storage layer,
// used to avoid a direct dependency on the storage package.
type StorageAction struct {
	ID                string
	CreatedAt         int64
	Title             string
	Type              string
	State             string
	TargetPlatform    string
	ContentSubject    string
	ContentMessage    string
	ContentBlobURLs   string
	Keywords          string
	ReachedIndex      int
	ScheduledDate     string
	StartDate         string
	EndDate           string
	ExecutionInterval int
	CampaignID        string
	Params            map[string]interface{}
}

// ---------------------------------------------------------------------------
// Step handler signature
// ---------------------------------------------------------------------------

// StepHandler is the function signature for individual step implementations.
type StepHandler func(ctx context.Context, step StepDef) (*StepResult, error)

// ---------------------------------------------------------------------------
// ActionExecutor
// ---------------------------------------------------------------------------

// ActionExecutor orchestrates the execution of a loaded action definition
// against a browser page.
type ActionExecutor struct {
	// SuccessfulStep observes confirmed steps without changing execution or retry behavior.
	SuccessfulStep func(StepDef, *StepResult)

	ctx          context.Context
	page         browser.PageInterface
	db           StorageInterface
	configMgr    ConfigInterface
	events       chan<- ExecutionEvent
	botAdapter   BotAdapter
	logger       zerolog.Logger
	execCtx      *ExecutionContext
	resolver     *VariableResolver
	errorHandler *ErrorHandler
	handlers     map[string]StepHandler
	action       *StorageAction
	actionDef    *ActionDef
	startTime    time.Time
	// reachedIndexByLoop tracks per-loop progress within a single Execute
	// run so that multiple loops (or a re-triggered loop) do not share one
	// reached index.
	reachedIndexByLoop map[string]int
	// Jev element-picker fallback (jevfallback.go); nil client = off.
	jevClient *jev.Client
	jevMinP   float64
	jevMarker string // marker set by the current step's pick, if any

	// Package-era state (package_ctx.go). pkg is nil for legacy actions.
	pkg      PackageContext
	selObs   SelectorObserver
	safeMode bool
	// safeStop is set when safe mode stopped before a side-effect step.
	safeStop *SafeStop
	// fatal is set by failRun (safe_mode.go): the run must unwind.
	fatal            error
	downloadsAllowed bool
	// selMarkers are in-page marker tokens set by package-selector lookups
	// (selectors_pkg.go) during the current step; removed when it ends.
	selMarkers []string
}

// NewActionExecutor creates a fully initialised executor. The page must already
// be navigated to the platform's domain (or will be by the first navigate
// step). events may be nil if no external monitoring is needed.
func NewActionExecutor(
	ctx context.Context,
	page browser.PageInterface,
	db StorageInterface,
	configMgr ConfigInterface,
	events chan<- ExecutionEvent,
	botAdapter BotAdapter,
	logger zerolog.Logger,
) *ActionExecutor {
	execCtx := NewExecutionContext()
	ae := &ActionExecutor{
		ctx:                ctx,
		page:               page,
		db:                 db,
		configMgr:          configMgr,
		events:             events,
		botAdapter:         botAdapter,
		logger:             logger.With().Str("component", "executor").Logger(),
		execCtx:            execCtx,
		resolver:           NewVariableResolver(execCtx),
		errorHandler:       NewErrorHandler(),
		handlers:           make(map[string]StepHandler),
		reachedIndexByLoop: make(map[string]int),
	}
	ae.resolver.scope = ae.secretScope
	ae.resolver.declaredSecret = func(name string) bool { return ae.secretInputNames()[name] }
	ae.initHandlers()
	return ae
}

// SetJevPicker enables the Jev element-picker fallback: a step with an
// intent whose selector/xpath finds nothing asks Jev for the matching
// element, accepted only when its top probability is ≥ minP. A nil client
// disables it (the default).
func (ae *ActionExecutor) SetJevPicker(c *jev.Client, minP float64) {
	ae.jevClient = c
	ae.jevMinP = minP
}

// SetVariable pre-seeds a variable in the execution context before Execute() is
// called. Use this to pass user-supplied data (e.g., selectedListItems) into the
// action pipeline.
func (ae *ActionExecutor) SetVariable(key string, value interface{}) {
	ae.execCtx.SetVariable(key, value)
}

// initHandlers registers all step type handlers in the dispatch map.
func (ae *ActionExecutor) initHandlers() {
	ae.handlers["navigate"] = ae.stepNavigate
	ae.handlers["wait"] = ae.stepWait
	ae.handlers["refresh"] = ae.stepRefresh
	ae.handlers["find_element"] = ae.stepFindElement
	ae.handlers["click"] = ae.stepClick
	ae.handlers["type"] = ae.stepType
	ae.handlers["upload"] = ae.stepUpload
	ae.handlers["scroll"] = ae.stepScroll
	ae.handlers["hover"] = ae.stepHover
	ae.handlers["extract_text"] = ae.stepExtractText
	ae.handlers["extract_attribute"] = ae.stepExtractAttribute
	ae.handlers["extract_multiple"] = ae.stepExtractMultiple
	ae.handlers["condition"] = ae.stepCondition
	ae.handlers["update_progress"] = ae.stepUpdateProgress
	ae.handlers["save_data"] = ae.stepSaveData
	ae.handlers["mark_failed"] = ae.stepMarkFailed
	ae.handlers["log"] = ae.stepLog
	ae.handlers["call_bot_method"] = ae.stepCallBotMethod
	ae.handlers["set_variable"] = ae.stepSetVariable
	ae.initExtendedHandlers()
}

// Execute runs the complete action. It follows four phases:
//  1. Identify which steps belong to loops
//  2. Execute initial (non-loop) steps
//  3. Execute each loop
//  4. Aggregate and return results
func (ae *ActionExecutor) Execute(action *StorageAction) (*ExecutionResult, error) {
	actionDef, err := GetLoader().Load(action.TargetPlatform, action.Type)
	if err != nil {
		return nil, fmt.Errorf("loading action definition: %w", err)
	}
	return ae.ExecuteDef(action, actionDef)
}

// ExecuteDef runs an already-loaded definition (e.g. a recording draft that
// is not installed). The definition is validated first and refused on
// errors. In safe mode a run that stops before a side-effect step returns
// success; SafeStopped reports where.
func (ae *ActionExecutor) ExecuteDef(action *StorageAction, actionDef *ActionDef) (*ExecutionResult, error) {
	res, err := ae.executeDef(action, actionDef)
	return res, ae.redactErr(err)
}

func (ae *ActionExecutor) executeDef(action *StorageAction, actionDef *ActionDef) (*ExecutionResult, error) {
	ae.startTime = time.Now()
	ae.action = action
	ae.actionDef = actionDef

	if err := ae.prepareRun(action.TargetPlatform, actionDef); err != nil {
		return nil, err
	}
	actionDef = ae.actionDef // prepareRun may assign synthetic step ids

	// Seed the execution context with action fields.
	ae.seedVariables(action)
	ae.registerSecretInputs()

	// Validate declared required inputs before executing anything.
	if err := ae.validateRequiredInputs(actionDef); err != nil {
		ae.logger.Error().Err(err).Msg("required input validation failed")
		return nil, err
	}

	ae.emitEvent(ExecutionEvent{
		Type:     "action_start",
		ActionID: action.ID,
		Message:  fmt.Sprintf("Starting %s/%s", action.TargetPlatform, action.Type),
	})

	// Update state to RUNNING.
	if ae.db != nil {
		if err := ae.db.UpdateActionState(action.ID, "RUNNING"); err != nil {
			ae.logger.Warn().Err(err).Msg("failed to update action state to RUNNING")
		}
	}

	// Phase 1: Identify all step IDs that are referenced by loops.
	loopStepIDs := make(map[string]bool)
	for _, loop := range actionDef.Loops {
		for _, id := range ae.getAllReferencedIDs(loop, actionDef.Steps) {
			loopStepIDs[id] = true
		}
	}

	// Identify all step IDs referenced only in condition then/else branches.
	// These should not be included in initialSteps — the condition step executes
	// them directly via executeSteps when the branch is taken.
	conditionBranchIDs := make(map[string]bool)
	var collectBranches func(steps []StepDef)
	collectBranches = func(steps []StepDef) {
		for _, step := range steps {
			for _, id := range step.Then {
				conditionBranchIDs[id] = true
			}
			for _, id := range step.Else {
				conditionBranchIDs[id] = true
			}
			// A condition inside a for_each body may branch to top-level
			// steps too; those must not also run as initial steps.
			collectBranches(step.Steps)
		}
	}
	collectBranches(actionDef.Steps)

	// Phase 2: Execute initial (non-loop, non-condition-branch) steps in order.
	var initialSteps []StepDef
	for _, step := range actionDef.Steps {
		if !loopStepIDs[step.ID] && !conditionBranchIDs[step.ID] {
			initialSteps = append(initialSteps, step)
		}
	}

	if err := ae.executeSteps(ae.ctx, initialSteps); err != nil && !errors.Is(err, errSafeStop) {
		if errors.Is(err, ErrAbort) {
			ae.logger.Error().Msg("action aborted during initial steps")
			if ae.db != nil {
				if serr := ae.db.UpdateActionState(action.ID, "FAILED"); serr != nil {
					ae.logger.Error().Err(serr).Str("actionID", action.ID).Msg("failed to persist action state FAILED — action may appear stuck")
				}
			}
			return ae.buildResult(), err
		}
		if isContextError(err) {
			ae.logger.Error().Err(err).Msg("action cancelled/timed out during initial steps")
			if ae.db != nil {
				if serr := ae.db.UpdateActionState(action.ID, "FAILED"); serr != nil {
					ae.logger.Error().Err(serr).Str("actionID", action.ID).Msg("failed to persist action state FAILED — action may appear stuck")
				}
			}
			return ae.buildResult(), err
		}
		ae.logger.Warn().Err(err).Msg("error during initial steps (continuing to loops)")
	}

	// Phase 3: Execute loops.
	for _, loop := range actionDef.Loops {
		if ae.safeStop != nil {
			break
		}
		if err := ae.executeLoop(ae.ctx, loop, actionDef.Steps); err != nil && !errors.Is(err, errSafeStop) {
			if errors.Is(err, ErrAbort) {
				ae.logger.Error().Str("loopID", loop.ID).Msg("action aborted during loop")
				if ae.db != nil {
					if serr := ae.db.UpdateActionState(action.ID, "FAILED"); serr != nil {
						ae.logger.Error().Err(serr).Str("actionID", action.ID).Msg("failed to persist action state FAILED — action may appear stuck")
					}
				}
				return ae.buildResult(), err
			}
			if isContextError(err) {
				ae.logger.Error().Err(err).Str("loopID", loop.ID).Msg("action cancelled/timed out during loop")
				if ae.db != nil {
					if serr := ae.db.UpdateActionState(action.ID, "FAILED"); serr != nil {
						ae.logger.Error().Err(serr).Str("actionID", action.ID).Msg("failed to persist action state FAILED — action may appear stuck")
					}
				}
				return ae.buildResult(), err
			}
			ae.logger.Warn().Err(err).Str("loopID", loop.ID).Msg("loop completed with errors")
		}
	}

	// Phase 4: Determine the final state honestly. Individual steps that hit
	// the "mark_failed" onError action (or exhaust retries with no other
	// onFailure policy — see errors.go's handleOnFailure default) don't abort
	// the run — by design, later steps and loops still execute — but they DO
	// record a FailedItem. Reaching here with any FailedItems recorded means
	// the action ran to completion while failing to do the thing it was
	// actually asked to do (e.g. Gemini's generate_image action: every
	// fallback tier for typing/sending/waiting failed), so it must be
	// reported as FAILED, not COMPLETED — previously this branch always
	// reported COMPLETED and returned a nil error regardless of FailedItems,
	// which is why a workflow node wrapping this executor (BrowserNode.Execute)
	// could show green/success even though nothing it was asked to do worked.
	if ae.execCtx.HasFailedItems() {
		result := ae.buildResult()
		if ae.db != nil {
			if serr := ae.db.UpdateActionState(action.ID, "FAILED"); serr != nil {
				ae.logger.Error().Err(serr).Str("actionID", action.ID).Msg("failed to persist action state FAILED — action may appear stuck RUNNING")
			}
		}
		summary := result.FailedItems[len(result.FailedItems)-1]
		ae.emitEvent(ExecutionEvent{
			Type:     "action_failed",
			ActionID: action.ID,
			Message:  fmt.Sprintf("%s/%s failed: %d step(s) failed, last: %s: %v", action.TargetPlatform, action.Type, len(result.FailedItems), summary.StepID, summary.Error),
		})
		return result, fmt.Errorf("action %s/%s: %d step(s) failed, last: %s: %w", action.TargetPlatform, action.Type, len(result.FailedItems), summary.StepID, summary.Error)
	}

	if ae.db != nil {
		if serr := ae.db.UpdateActionState(action.ID, "COMPLETED"); serr != nil {
			ae.logger.Error().Err(serr).Str("actionID", action.ID).Msg("failed to persist action state COMPLETED — action may appear stuck RUNNING")
		}
	}

	ae.emitEvent(ExecutionEvent{
		Type:     "action_complete",
		ActionID: action.ID,
		Message:  fmt.Sprintf("Completed %s/%s", action.TargetPlatform, action.Type),
	})

	return ae.buildResult(), nil
}

// isContextError reports whether err is a context cancellation or deadline
// exhaustion (possibly wrapped), used to persist an honest FAILED state
// instead of COMPLETED for cancelled/timed-out runs.
func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// seedVariables populates the execution context with initial values from the
// storage action.
func (ae *ActionExecutor) seedVariables(action *StorageAction) {
	ae.execCtx.SetVariable("actionId", action.ID)
	ae.execCtx.SetVariable("actionType", action.Type)
	ae.execCtx.SetVariable("platform", action.TargetPlatform)
	ae.execCtx.SetVariable("reachedIndex", action.ReachedIndex)

	if action.ContentMessage != "" {
		ae.execCtx.SetVariable("messageText", action.ContentMessage)
		ae.execCtx.SetVariable("contentMessage", action.ContentMessage)
		ae.execCtx.SetVariable("commentText", action.ContentMessage)
		ae.execCtx.SetVariable("replyText", action.ContentMessage)
		ae.execCtx.SetVariable("text", action.ContentMessage)
	}
	if action.ContentSubject != "" {
		ae.execCtx.SetVariable("messageSubject", action.ContentSubject)
		ae.execCtx.SetVariable("contentSubject", action.ContentSubject)
	}
	if action.Keywords != "" {
		ae.execCtx.SetVariable("keyword", action.Keywords)
		ae.execCtx.SetVariable("keywords", action.Keywords)
		// URL-safe version for embedding in URL templates.
		ae.execCtx.SetVariable("keywordEncoded", url.QueryEscape(action.Keywords))
	}
	if action.ContentBlobURLs != "" {
		ae.execCtx.SetVariable("contentBlobUrls", action.ContentBlobURLs)
	}
	// Seed scheduling fields consumed by date-bounded actions (auto_reply_dms).
	if action.StartDate != "" {
		ae.execCtx.SetVariable("startDate", action.StartDate)
	}
	if action.EndDate != "" {
		ae.execCtx.SetVariable("endDate", action.EndDate)
	}
	if action.ExecutionInterval > 0 {
		ae.execCtx.SetVariable("pollInterval", action.ExecutionInterval)
	}
	// Seed all custom params as execution variables.
	for k, v := range action.Params {
		ae.execCtx.SetVariable(k, v)
	}
}

// validateRequiredInputs checks that every input declared as required by the
// action definition is present (non-empty) in the execution context. Required
// entries may be plain strings (legacy format) or objects with a "name" field.
//
// Inputs that declare "aliases" are filled from the first non-empty alias
// first (see resolveInputAliases), so a renamed input keeps accepting the
// config field older workflows set.
func (ae *ActionExecutor) validateRequiredInputs(def *ActionDef) error {
	if def.Inputs == nil {
		return nil
	}
	ae.resolveInputAliases(def.Inputs)
	var missing []string
	for _, raw := range def.Inputs.Required {
		name := requiredInputName(raw)
		if name == "" {
			continue
		}
		v, ok := ae.execCtx.GetVariable(name)
		if !ok || v == nil {
			missing = append(missing, name)
			continue
		}
		if isEmptyRequiredValue(v) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	for i, name := range missing {
		if canonical, ok := canonicalInputNames[name]; ok {
			missing[i] = canonical
		}
	}
	if len(missing) == 1 {
		return fmt.Errorf("missing required input '%s'", missing[0])
	}
	return fmt.Errorf("missing required inputs '%s'", strings.Join(missing, "', '"))
}

// canonicalInputNames maps a required input's legacy definition name to
// the name a node config sets it by, for error messages. The browser node
// fills selectedListItems from its targets list, so reporting
// "selectedListItems" sent readers looking for a field they never set.
var canonicalInputNames = map[string]string{"selectedListItems": "targets"}

// isEmptyRequiredValue reports whether a present value still amounts to
// "nothing was supplied" for a required input.
//
// Checking only for the empty string used to let two shapes through, both of
// which then ran an action that did nothing and reported success:
//
//   - An empty collection. `prompts: []` satisfies "present and not empty
//     string", and a loop over it runs zero times.
//   - The four characters "null". A node config of {{ json $json.prompts }}
//     renders exactly that when the field is absent, because that is what
//     json.Marshal(nil) produces — so the workflow that failed here passed a
//     required "prompts" of "null", iterated nothing, generated no images,
//     and finished green. A required input is never legitimately the literal
//     "null"; that string only ever means an expression found nothing.
func isEmptyRequiredValue(v interface{}) bool {
	switch val := v.(type) {
	case string:
		s := strings.TrimSpace(val)
		return s == "" || s == "null"
	case []interface{}:
		return len(val) == 0
	case []string:
		return len(val) == 0
	case []map[string]interface{}:
		return len(val) == 0
	case map[string]interface{}:
		return len(val) == 0
	}
	return false
}

// resolveInputAliases sets every declared input that is missing or empty
// from the first of its "aliases" that holds a value. A list value (the
// node's targets) fills a scalar input with its first entry, taking a
// map entry's url, href or username.
func (ae *ActionExecutor) resolveInputAliases(in *InputDef) {
	for _, raw := range append(append([]json.RawMessage{}, in.Required...), in.Optional...) {
		var obj struct {
			Name    string   `json:"name"`
			Type    string   `json:"type"`
			Aliases []string `json:"aliases"`
		}
		if json.Unmarshal(raw, &obj) != nil || obj.Name == "" || len(obj.Aliases) == 0 {
			continue
		}
		if v, ok := ae.execCtx.GetVariable(obj.Name); ok && v != nil && !isEmptyRequiredValue(v) {
			continue
		}
		for _, alias := range obj.Aliases {
			v, ok := ae.execCtx.GetVariable(alias)
			if !ok || v == nil || isEmptyRequiredValue(v) {
				continue
			}
			if obj.Type != "list" && obj.Type != "array" {
				v = firstListEntry(v)
			}
			if v == nil || isEmptyRequiredValue(v) {
				continue
			}
			ae.execCtx.SetVariable(obj.Name, v)
			break
		}
	}
}

// firstListEntry reduces a list to its first entry (a map entry to its url,
// href or username); any other value is returned unchanged.
func firstListEntry(v interface{}) interface{} {
	var first interface{}
	switch l := v.(type) {
	case []interface{}:
		first = l[0]
	case []string:
		first = l[0]
	case []map[string]interface{}:
		first = l[0]
	default:
		return v
	}
	if m, ok := first.(map[string]interface{}); ok {
		for _, k := range []string{"url", "href", "username"} {
			if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
		return nil
	}
	return first
}

// requiredInputName extracts the input name from a required-input entry,
// accepting both the legacy string form ("messageText") and the object form
// ({"name": "messageText", ...}).
func requiredInputName(raw json.RawMessage) string {
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		return strings.TrimSpace(name)
	}
	var obj struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return strings.TrimSpace(obj.Name)
	}
	return ""
}

// executeSteps runs a slice of steps sequentially. It handles variable
// resolution, error handling, and retries for each step.
func (ae *ActionExecutor) executeSteps(ctx context.Context, steps []StepDef) error {
	for i := 0; i < len(steps); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := ae.haltErr(); err != nil {
			return err
		}
		step := steps[i]
		if ae.safeMode && ae.safeStopRequired(step) {
			if err := ae.precheckSafeStop(ctx, step); err != nil {
				return ae.failRun(step.ID, err)
			}
			return ae.stopBeforeSideEffect(step)
		}

		// Resolve templates in the step definition.
		resolved := ae.resolver.ResolveStepDef(step)

		handler, ok := ae.handlers[resolved.Type]
		if !ok {
			return ae.failRun(resolved.ID, fmt.Errorf("unknown step type %q", resolved.Type))
		}

		ae.emitEvent(ExecutionEvent{
			Type:     "step_start",
			ActionID: ae.action.ID,
			StepID:   resolved.ID,
			Index:    i,
			Total:    len(steps),
			Message:  fmt.Sprintf("Executing step %s (%s)", resolved.ID, resolved.Type),
		})

		result, err := handler(ctx, resolved)
		if err == nil && result != nil && result.Success && result.Error == nil && !result.Skip && !result.Abort && ae.SuccessfulStep != nil {
			ae.SuccessfulStep(resolved, result)
		}
		result, err = ae.applyUntil(ctx, resolved, result, err)
		result, err = ae.redactStep(result, err)
		if herr := ae.afterStep(resolved, result, err); herr != nil {
			return herr
		}
		if err != nil || (result != nil && !result.Success) {
			if result == nil {
				result = &StepResult{
					Success: false,
					StepID:  resolved.ID,
					Error:   err,
				}
			}
			result.StepID = resolved.ID

			// Apply error handler if defined.
			onErr := resolved.OnError
			if onErr == nil {
				onErr = ae.defaultOnError()
			}
			handled := ae.errorHandler.Handle(ctx, onErr, result, ae.execCtx)

			if handled.Abort {
				if errors.Is(handled.Error, ErrAbort) {
					return handled.Error
				}
				return ErrAbort
			}
			if handled.Retry {
				// Re-execute the same step.
				i--
				continue
			}
			if handled.Skip {
				skipLog := ae.logger.Debug().Str("stepID", resolved.ID)
				if result != nil && result.Error != nil {
					skipLog = skipLog.Err(result.Error)
				}
				skipLog.Msg("step skipped after error handling")
				ae.execCtx.SetStepResult(resolved.ID, handled)
				continue
			}

			// For "continue" error action, record and move on.
			ae.execCtx.SetStepResult(resolved.ID, handled)
			continue
		}

		// Step succeeded.
		if result != nil {
			result.StepID = resolved.ID
			ae.execCtx.SetStepResult(resolved.ID, result)

			// Handle onSuccess callback.
			if resolved.OnSuccess != nil {
				ae.handleOnSuccess(resolved.OnSuccess)
			}
		}

		// Reset retry counter on success.
		ae.errorHandler.ResetRetries(resolved.ID)

		ae.emitEvent(ExecutionEvent{
			Type:     "step_complete",
			ActionID: ae.action.ID,
			StepID:   resolved.ID,
			Index:    i,
			Total:    len(steps),
		})
	}
	return nil
}

// handleOnSuccess processes the onSuccess callback of a step.
func (ae *ActionExecutor) handleOnSuccess(sa *SuccessAction) {
	switch sa.Action {
	case "set_variable":
		if sa.Variable != "" {
			ae.execCtx.SetVariable(sa.Variable, sa.Value)
		}
	case "increment":
		varName := sa.Increment
		if varName == "" {
			varName = sa.Variable
		}
		if varName != "" {
			val, _ := ae.execCtx.GetVariable(varName)
			switch v := val.(type) {
			case int:
				ae.execCtx.SetVariable(varName, v+1)
			case float64:
				ae.execCtx.SetVariable(varName, v+1)
			default:
				ae.execCtx.SetVariable(varName, 1)
			}
		}
	case "update_progress":
		if sa.Increment != "" {
			val, _ := ae.execCtx.GetVariable(sa.Increment)
			switch v := val.(type) {
			case int:
				ae.execCtx.SetVariable(sa.Increment, v+1)
			case float64:
				ae.execCtx.SetVariable(sa.Increment, v+1)
			default:
				ae.execCtx.SetVariable(sa.Increment, 1)
			}
		}
	case "save_data":
		// Trigger a flush of extracted items.
		if ae.db != nil && ae.action != nil && len(ae.execCtx.ExtractedItems) > 0 {
			ae.execCtx.mu.Lock()
			items := make([]map[string]interface{}, len(ae.execCtx.ExtractedItems))
			copy(items, ae.execCtx.ExtractedItems)
			ae.execCtx.mu.Unlock()
			if err := ae.db.SaveExtractedData(ae.action.ID, ae.redactItems(items)); err != nil {
				ae.logger.Warn().Err(err).Msg("failed to save extracted data on success callback")
			}
		}
	}
}

// executeLoop runs a single loop definition. It resolves the iterator to a
// slice, then executes the loop's steps for each item starting from
// the action's reached index. If the loop declares a maxItems cap, at most
// that many items are processed per session before the loop exits with a
// "cap reached" note.
func (ae *ActionExecutor) executeLoop(ctx context.Context, loop LoopDef, allSteps []StepDef) error {
	// Resolve the iterator to get the collection to iterate over.
	items := ae.resolveIterator(loop.Iterator)
	if items == nil {
		ae.logger.Warn().
			Str("loopID", loop.ID).
			Str("iterator", loop.Iterator).
			Msg("loop iterator resolved to nil, skipping loop")
		return nil
	}

	collection := toSlice(items)
	ae.execCtx.enterLoop()
	defer ae.execCtx.leaveLoop()
	if len(collection) == 0 {
		// toSlice yields nil both for a genuinely empty collection and for a
		// value that simply isn't one (a string, a number). Only the first is
		// legitimately zero work; the second is a misconfigured iterator that
		// otherwise runs no steps, produces nothing, and still reports
		// success — how a Gemini batch whose prompts rendered to the string
		// "null" came back green with no images.
		if !isIterable(items) {
			return fmt.Errorf("loop %q: iterator %q resolved to %T (%v), which cannot be iterated — expected an array",
				loop.ID, loop.Iterator, items, truncateForError(items))
		}
		ae.logger.Info().Str("loopID", loop.ID).Msg("loop collection is empty")
		return nil
	}

	// Determine the starting index (for resume support), keyed per loop ID:
	// a loop that has already run in this execution resumes from its own
	// progress; a loop's first run seeds from the action-level reached index.
	startIdx := 0
	if reached, ok := ae.reachedIndexByLoop[loop.ID]; ok {
		startIdx = reached
	} else if ae.action != nil && ae.action.ReachedIndex > 0 {
		startIdx = ae.action.ReachedIndex
	}

	// Resolve the per-session item cap (0 or unresolvable → unlimited).
	maxItems := ae.resolveLoopCap(loop.MaxItems)

	// Resolve the daily cap (0 or unresolvable → unlimited) and the profile
	// identity it's tracked against. The daily counter persists across
	// separate action runs (unlike maxItems, which resets every run), so it
	// requires storage; with no db configured (e.g. some tests) the daily
	// cap is a no-op, same as the session cap already is in that case.
	dailyCap := ae.resolveLoopCap(loop.MaxItemsPerDay)

	loopSteps := ae.getStepsByIDs(allSteps, loop.Steps)

	ae.logger.Info().
		Str("loopID", loop.ID).
		Int("total", len(collection)).
		Int("startIndex", startIdx).
		Int("maxItems", maxItems).
		Int("dailyCap", dailyCap).
		Int("stepCount", len(loopSteps)).
		Msg("starting loop execution")

	processed := 0
	for idx := startIdx; idx < len(collection); idx++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Per-session cap: stop before processing more items than allowed.
		if maxItems > 0 && processed >= maxItems {
			ae.noteLoopCapReached(loop, maxItems, idx)
			ae.reachedIndexByLoop[loop.ID] = idx
			// Persist the cap boundary so a later run (e.g. with a raised
			// cap) resumes from here instead of redoing capped items.
			if ae.db != nil && ae.action != nil {
				if err := ae.db.UpdateActionReachedIndex(ae.action.ID, idx); err != nil {
					ae.logger.Warn().Err(err).Int("index", idx).Msg("failed to update reached index at cap")
				}
			}
			return nil
		}

		// Daily cap: stop before processing more items than allowed today
		// (UTC), across however many separate action runs got here.
		if dailyCap > 0 && ae.db != nil && ae.action != nil {
			count, err := ae.db.GetDailyActionCount(ae.action.Type)
			if err != nil {
				ae.logger.Warn().Err(err).Msg("failed to read daily action count; treating as unlimited for this iteration")
			} else if count >= dailyCap {
				ae.noteDailyCapReached(loop, dailyCap, count, idx)
				ae.reachedIndexByLoop[loop.ID] = idx
				if err := ae.db.UpdateActionReachedIndex(ae.action.ID, idx); err != nil {
					ae.logger.Warn().Err(err).Int("index", idx).Msg("failed to update reached index at daily cap")
				}
				return nil
			}
		}

		item := collection[idx]

		// Set loop variables.
		ae.execCtx.SetVariable("item", item)
		ae.execCtx.SetVariable(loop.IndexVar, idx)
		ae.execCtx.SetVariable("loopIndex", idx)
		ae.execCtx.SetVariable("loopTotal", len(collection))

		ae.emitEvent(ExecutionEvent{
			Type:     "loop_iteration",
			ActionID: ae.action.ID,
			StepID:   loop.ID,
			Index:    idx,
			Total:    len(collection),
			Message:  fmt.Sprintf("Loop %s: item %d/%d", loop.ID, idx+1, len(collection)),
		})

		stepErr := ae.executeSteps(ctx, loopSteps)
		if stepErr != nil {
			if isHalt(stepErr) {
				return stepErr
			}
			ae.logger.Warn().
				Err(stepErr).
				Int("index", idx).
				Str("loopID", loop.ID).
				Msg("loop iteration failed")
		}

		// Only a successfully completed iteration counts against the daily
		// cap — a failed step (e.g. a follow button that never appeared)
		// didn't actually perform the rate-limited action.
		if stepErr == nil && dailyCap > 0 && ae.db != nil && ae.action != nil {
			if _, err := ae.db.IncrementDailyActionCount(ae.action.Type); err != nil {
				ae.logger.Warn().Err(err).Msg("failed to increment daily action count")
			}
		}

		processed++
		ae.reachedIndexByLoop[loop.ID] = idx + 1

		// Update reached index in storage for resume support.
		// Batch writes: persist every 50 iterations or on the last item to avoid N+1 DB writes.
		if ae.db != nil && ae.action != nil {
			if (idx+1)%50 == 0 || idx == len(collection)-1 {
				if err := ae.db.UpdateActionReachedIndex(ae.action.ID, idx+1); err != nil {
					ae.logger.Warn().Err(err).Int("index", idx+1).Msg("failed to update reached index")
				}
			}
		}
	}

	return nil
}

// resolveLoopCap resolves a loop's maxItems template (e.g.
// "{{maxMessagesPerSession or 30}}") to an integer cap. Empty, missing, or
// non-numeric values resolve to 0, meaning unlimited.
func (ae *ActionExecutor) resolveLoopCap(template string) int {
	if strings.TrimSpace(template) == "" {
		return 0
	}
	return toInt(ae.resolver.ResolveValue(template))
}

// toInt coerces common numeric types (including numeric strings from CLI
// params) to an int, returning 0 for anything unrecognised.
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0
		}
		return i
	default:
		return 0
	}
}

// noteLoopCapReached records that a loop exited early because its per-session
// cap was reached. The note is logged, emitted as an event for monitors, and
// stored in the execution context (Data + Variables) so it surfaces in the
// action's result metadata.
func (ae *ActionExecutor) noteLoopCapReached(loop LoopDef, cap, idx int) {
	note := map[string]interface{}{
		"loopId":     loop.ID,
		"cap":        cap,
		"startIndex": idx,
		"reason":     "per-session cap reached",
	}
	ae.execCtx.SetData("loopCapReached", note)
	ae.execCtx.SetVariable("capReached", loop.ID)
	ae.logger.Info().
		Str("loopID", loop.ID).
		Int("cap", cap).
		Int("index", idx).
		Msg("loop stopped: per-session cap reached")
	ae.emitEvent(ExecutionEvent{
		Type:     "loop_cap_reached",
		ActionID: ae.action.ID,
		StepID:   loop.ID,
		Index:    idx,
		Message:  fmt.Sprintf("Loop %s stopped at item %d: per-session cap of %d reached", loop.ID, idx, cap),
	})
}

// noteDailyCapReached records that a loop exited early because its
// persisted daily cap (across action runs, resetting at UTC midnight) was
// reached — the counterpart to noteLoopCapReached's per-session cap.
func (ae *ActionExecutor) noteDailyCapReached(loop LoopDef, cap, count, idx int) {
	note := map[string]interface{}{
		"loopId":     loop.ID,
		"cap":        cap,
		"count":      count,
		"startIndex": idx,
		"reason":     "daily cap reached",
	}
	ae.execCtx.SetData("dailyCapReached", note)
	ae.execCtx.SetVariable("dailyCapReached", loop.ID)
	ae.logger.Info().
		Str("loopID", loop.ID).
		Int("cap", cap).
		Int("count", count).
		Int("index", idx).
		Msg("loop stopped: daily cap reached")
	ae.emitEvent(ExecutionEvent{
		Type:     "loop_cap_reached",
		ActionID: ae.action.ID,
		StepID:   loop.ID,
		Index:    idx,
		Message:  fmt.Sprintf("daily cap reached: %d/%d %s today", count, cap, ae.action.Type),
	})
}

// resolveIterator resolves the loop iterator path to its underlying value.
func (ae *ActionExecutor) resolveIterator(iteratorPath string) interface{} {
	return ae.resolver.ResolvePath(iteratorPath)
}

// toSlice converts various collection types to []interface{}.
// isIterable reports whether val is one of the collection shapes toSlice
// understands — i.e. whether an empty result from toSlice means "no items"
// rather than "not a collection at all".
func isIterable(val interface{}) bool {
	switch val.(type) {
	case []interface{}, []string, []map[string]interface{}:
		return true
	}
	return false
}

// truncateForError keeps a misconfigured iterator's value readable in an
// error without pasting a whole document into it.
func truncateForError(v interface{}) string {
	s := fmt.Sprintf("%v", v)
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

func toSlice(val interface{}) []interface{} {
	switch v := val.(type) {
	case []interface{}:
		return v
	case []string:
		result := make([]interface{}, len(v))
		for i, s := range v {
			result[i] = s
		}
		return result
	case []map[string]interface{}:
		result := make([]interface{}, len(v))
		for i, m := range v {
			result[i] = m
		}
		return result
	default:
		return nil
	}
}

// getAllReferencedIDs collects all step IDs referenced by a loop, including
// those in then/else branches of condition steps.
func (ae *ActionExecutor) getAllReferencedIDs(loop LoopDef, allSteps []StepDef) []string {
	// Build a lookup map once instead of scanning allSteps per ID.
	stepMap := make(map[string]StepDef, len(allSteps))
	for _, step := range allSteps {
		stepMap[step.ID] = step
	}

	seen := make(map[string]bool)
	// Iterative BFS instead of recursion to avoid stack overflow on deeply nested actions.
	queue := make([]string, len(loop.Steps))
	copy(queue, loop.Steps)

	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		if step, ok := stepMap[id]; ok {
			if len(step.Then) > 0 {
				queue = append(queue, step.Then...)
			}
			if len(step.Else) > 0 {
				queue = append(queue, step.Else...)
			}
		}
	}

	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	return result
}

// getStepsByIDs returns step definitions from allSteps matching the given IDs,
// preserving the order of the ids slice.
func (ae *ActionExecutor) getStepsByIDs(allSteps []StepDef, ids []string) []StepDef {
	stepMap := make(map[string]StepDef, len(allSteps))
	for _, step := range allSteps {
		stepMap[step.ID] = step
	}

	result := make([]StepDef, 0, len(ids))
	for _, id := range ids {
		if step, ok := stepMap[id]; ok {
			result = append(result, step)
		} else {
			ae.logger.Warn().
				Str("stepID", id).
				Msg("referenced step id has no definition, dropping reference")
		}
	}
	return result
}

// emitEvent sends an execution event to the events channel. If the channel is
// nil or full, the event is silently dropped.
func (ae *ActionExecutor) emitEvent(event ExecutionEvent) {
	if ae.events == nil {
		return
	}
	event.Message = ae.redact(event.Message)
	select {
	case ae.events <- event:
	default:
		ae.logger.Debug().
			Str("type", event.Type).
			Str("stepID", event.StepID).
			Msg("event channel full, dropping event")
	}
}

// buildResult aggregates the execution context into a final ExecutionResult.
func (ae *ActionExecutor) buildResult() *ExecutionResult {
	ae.execCtx.mu.Lock()
	defer ae.execCtx.mu.Unlock()

	extracted := ae.redactItems(ae.execCtx.ExtractedItems)
	if extracted == nil {
		extracted = []map[string]interface{}{}
	}

	failed := make([]FailedItem, len(ae.execCtx.FailedItems))
	copy(failed, ae.execCtx.FailedItems)
	for i := range failed {
		failed[i].Error = ae.redactErr(failed[i].Error)
	}

	return &ExecutionResult{
		ExtractedItems: extracted,
		ListOutput:     ae.execCtx.ListOutput,
		FailedItems:    failed,
		TotalProcessed: len(extracted) + len(failed),
		Duration:       time.Since(ae.startTime),
	}
}

// ValidateActionInputs reports whether act supplies every input that
// platform/actionType declares required, plus any extras the caller has
// already resolved (selectedListItems and friends, keyed as the definition
// names them).
//
// It builds no browser session and runs no step, so a caller can check a
// misconfigured node before paying for a page: internal/nodes'
// BrowserNode.Execute otherwise opens a tab, then discovers the action had
// nothing to work with.
func ValidateActionInputs(platform, actionType string, act *StorageAction, extras map[string]interface{}) error {
	def, err := GetLoader().Load(platform, actionType)
	if err != nil {
		return fmt.Errorf("loading action definition: %w", err)
	}
	ae := NewActionExecutor(context.Background(), nil, nil, nil, nil, nil, zerolog.Nop())
	ae.seedVariables(act)
	for k, v := range extras {
		ae.SetVariable(k, v)
	}
	return ae.validateRequiredInputs(def)
}
