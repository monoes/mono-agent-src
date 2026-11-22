package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/monoes/mono-agent/internal/hilsuggest"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/nodes/decisionclient"
	"github.com/monoes/mono-agent/internal/workflow"
)

// rowPlan is auto_decide's outcome for one row: a status to insert with
// ("" = pending) and keys merged into the row's node_config.
type rowPlan struct {
	status string
	extra  map[string]interface{}
}

// autoDecideCfg is the parsed auto_decide config.
type autoDecideCfg struct {
	policy       string
	provider     string // "" = the hil surface's provider, else the default
	approveAbove float64
	rejectAbove  float64 // 0 = never auto-reject
}

// autoDecideHeld is why auto_decide left an item of a workflow the user
// hasn't approved pending: it lets a message to a person go out.
const autoDecideHeld = "a workflow you haven't approved can't settle a step that sends a message to a person: it waits for you"

// autoDecideTimeout bounds all Jev calls of one node run.
const autoDecideTimeout = 30 * time.Second

func parseAutoDecide(ctxDB *sql.DB, profileID string, config map[string]interface{}) (*autoDecideCfg, error) {
	raw, ok := config["auto_decide"]
	if !ok || raw == nil {
		return nil, nil
	}
	// The GUI's JSON editor may hand the object over as text.
	if str, ok := raw.(string); ok {
		if strings.TrimSpace(str) == "" {
			return nil, nil
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(str), &parsed); err != nil {
			return nil, fmt.Errorf("%w: human_in_loop: auto_decide is not a JSON object: %v", workflow.ErrInvalidConfig, err)
		}
		raw = parsed
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: human_in_loop: auto_decide must be an object", workflow.ErrInvalidConfig)
	}
	c := &autoDecideCfg{}
	if p, ok := m["policy"].(string); ok {
		c.policy = strings.TrimSpace(p)
	}
	if p, ok := m["provider"]; ok && p != nil {
		name, ok := p.(string)
		if !ok {
			return nil, fmt.Errorf("%w: human_in_loop: auto_decide.provider must be a string", workflow.ErrInvalidConfig)
		}
		c.provider = strings.TrimSpace(name)
	}
	num := func(key string) (float64, bool, error) {
		v, ok := m[key]
		if !ok || v == nil {
			return 0, false, nil
		}
		var f float64
		switch t := v.(type) {
		case float64:
			f = t
		case int:
			f = float64(t)
		default:
			return 0, false, fmt.Errorf("%w: human_in_loop: auto_decide.%s must be a number", workflow.ErrInvalidConfig, key)
		}
		if f <= 0 || f > 1 {
			return 0, false, fmt.Errorf("%w: human_in_loop: auto_decide.%s = %v must be in (0,1]", workflow.ErrInvalidConfig, key, f)
		}
		return f, true, nil
	}
	f, set, err := num("approve_above")
	if err != nil {
		return nil, err
	}
	if set {
		c.approveAbove = f
	} else {
		c.approveAbove = jevconf.Threshold(ctxDB, profileID, jevconf.HIL, jevconf.DefaultThreshold[jevconf.HIL])
	}
	if c.rejectAbove, _, err = num("reject_above"); err != nil {
		return nil, err
	}
	return c, nil
}

// orgStarted reports whether the execution was started by an org (a grant
// or a message to an automation role). Its HIL rows are tier-routed org
// decisions (internal/orgdecide), so auto_decide never settles them.
func orgStarted(ctx context.Context, db *sql.DB, executionID string) bool {
	tt := workflow.TriggerTypeFrom(ctx)
	if tt == "" {
		_ = db.QueryRowContext(ctx, `SELECT COALESCE(trigger_type,'') FROM workflow_executions WHERE id = ?`, executionID).Scan(&tt)
	}
	return tt == workflow.TriggerTypeOrgTool || tt == workflow.TriggerTypeOrgMessage
}

// autoDecideRows applies the optional auto_decide config (plan WS5). It
// returns nil when auto_decide is absent — rows are then exactly today's.
// Otherwise every row gets a plan: skipped (surface off, no key, Jev
// error) ⇒ pending with the reason; confident approve (top p ≥
// approve_above) ⇒ approved; confident reject with reject_above set ⇒
// rejected; anything else ⇒ pending with the suggestion stored. Rows of
// org-started executions only ever get the suggestion.
func autoDecideRows(ctx context.Context, db *sql.DB, input workflow.NodeInput, config map[string]interface{}, profileID string, ro, ed []map[string]interface{}) ([]rowPlan, error) {
	cfg, err := parseAutoDecide(db, profileID, config)
	if err != nil || cfg == nil {
		return nil, err
	}
	plan := make([]rowPlan, len(input.Items))
	all := func(k string, v interface{}) {
		for i := range plan {
			if plan[i].extra == nil {
				plan[i].extra = map[string]interface{}{}
			}
			plan[i].extra[k] = v
		}
	}
	if cfg.policy != "" {
		all("policy", cfg.policy)
	}
	skip := func(reason string) ([]rowPlan, error) {
		zerolog.Ctx(ctx).Warn().Str("execution_id", input.ExecutionID).Str("node_id", input.NodeID).
			Msg("human_in_loop: auto_decide ignored: " + reason)
		all("auto_decide_skipped", reason)
		return plan, nil
	}
	if !jevconf.Enabled(db, profileID, jevconf.HIL) {
		return skip("TypeSafe Jev surface \"hil\" is not enabled for this profile (monoagentcli jev enable hil)")
	}
	client, _, err := decisionclient.New(ctx, db, profileID, decisionclient.Config{Provider: cfg.provider}, jevconf.HIL)
	if err != nil {
		return skip(err.Error())
	}
	typesafe := client.Provider == jev.ProviderTypeSafe
	kinds := watchProbability(client)
	ins := make([]hilsuggest.Input, len(input.Items))
	for i := range ins {
		ins[i] = hilsuggest.Input{Readonly: ro[i], Editable: ed[i], Policy: cfg.policy}
	}
	var sugs []hilsuggest.Suggestion
	var errs []error
	slow := slowProvider(client)
	if slow {
		sugs, errs = suggestEach(ctx, client, ins)
	} else {
		jctx, cancel := context.WithTimeout(ctx, autoDecideTimeout)
		defer cancel()
		sugs, errs = hilsuggest.SuggestAll(jctx, client, ins)
	}
	var firstErr error
	failed := make([]bool, len(plan))
	for i := range plan {
		if plan[i].extra == nil {
			plan[i].extra = map[string]interface{}{}
		}
		if errs[i] != nil {
			if firstErr == nil {
				firstErr = errs[i]
			}
			failed[i] = true
			if slow {
				// One item's failure leaves that item for a person, not the others.
				plan[i].extra["auto_decide_skipped"] = askFailure(client, errs[i])
			}
			continue
		}
		plan[i].extra["suggestion"] = sugs[i]
		if !typesafe { // D4: a TypeSafe row is as before decision providers
			plan[i].extra["provider"] = client.Provider
		}
	}
	if firstErr != nil && !slow {
		// Any Jev failure leaves every row for a person, as without Jev.
		return skip(askFailure(client, firstErr))
	}
	if orgStarted(ctx, db, input.ExecutionID) {
		all("auto_decide_skipped", "org-started execution: the org decides its HIL items")
		return plan, nil
	}
	if kind := kinds(); kind != "" && !typesafe {
		all("probability_kind", string(kind))
	}
	for i, s := range sugs {
		if failed[i] {
			continue
		}
		// D7: a provider without probabilities never settles an item.
		s.P = decisionclient.Gate(&jev.Response{Probability: kinds()}, s.P)
		switch {
		// Never auto-approve an item Jev itself rates high-risk: it waits for
		// a human with the suggestion shown.
		case s.Choice == hilsuggest.Approve && s.P >= cfg.approveAbove && s.Risk != "high":
			plan[i].status = "approved"
		case s.Choice == hilsuggest.Reject && cfg.rejectAbove > 0 && s.P >= cfg.rejectAbove:
			plan[i].status = "rejected"
		default:
			continue
		}
		plan[i].extra["decided_by"] = "jev:" + s.Model
		plan[i].extra["p"] = s.P
	}
	return plan, nil
}

// slowProvider reports whether client's provider answers slowly: it samples
// its probabilities, or takes one question per request (the HIL request has
// two). Its items get autoDecideTimeout each, and fail one by one.
func slowProvider(client *jev.Client) bool {
	if client.Provider == jev.ProviderTypeSafe || client.Backend == nil {
		return false
	}
	caps := client.Backend.Capabilities()
	return caps.Probability == jev.ProbSampled || caps.QuestionsPerRequest == 1
}

// suggestEach is hilsuggest.SuggestAll with a timeout per item.
func suggestEach(ctx context.Context, client *jev.Client, ins []hilsuggest.Input) ([]hilsuggest.Suggestion, []error) {
	out := make([]hilsuggest.Suggestion, len(ins))
	errs := make([]error, len(ins))
	sem := make(chan struct{}, hilsuggest.MaxInFlight)
	var wg sync.WaitGroup
	for i := range ins {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			ictx, cancel := context.WithTimeout(ctx, autoDecideTimeout)
			defer cancel()
			out[i], errs[i] = hilsuggest.Suggest(ictx, client, ins[i])
		}(i)
	}
	wg.Wait()
	return out, errs
}

// askFailure is the skip reason of a failed decision call: the error as
// before decision providers for TypeSafe, else a fixed phrase and the
// error's class (a provider's own error text may quote what its server
// answered, which a person or an agent reads here).
func askFailure(client *jev.Client, err error) string {
	if client.Provider == jev.ProviderTypeSafe {
		return "jev: " + err.Error()
	}
	return fmt.Sprintf("jev: decision provider %q did not answer (%s)", client.Provider, jevconf.ErrorClass(err))
}

// watchProbability records how client's answers got their probabilities. The
// returned func reports the kind they share, or jev.ProbNone when any answer
// had none (so the gate fails closed); "" before any answer.
func watchProbability(client *jev.Client) func() jev.ProbabilityKind {
	var mu sync.Mutex
	var kind jev.ProbabilityKind
	next := client.OnResult
	client.OnResult = func(req *jev.Request, resp *jev.Response, latency time.Duration, err error) {
		if next != nil {
			next(req, resp, latency, err)
		}
		if resp == nil || err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if kind == "" || resp.Probability == jev.ProbNone {
			kind = resp.Probability
		}
	}
	return func() jev.ProbabilityKind {
		mu.Lock()
		defer mu.Unlock()
		return kind
	}
}
