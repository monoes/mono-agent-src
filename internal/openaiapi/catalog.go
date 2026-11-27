package openaiapi

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/monomind"
)

// ErrUnknownModel means a model name does not resolve to a catalog entry.
var ErrUnknownModel = errors.New("unknown model")

// aliases are alternative runtime names accepted on input. The canonical id
// always uses the runtime id monomind reports.
var aliases = map[string]string{"agy": "antigravity"}

var (
	runtimeRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	// modelRE is the only shape of model id that may reach the runner's
	// command line: Exec does not validate it, and a leading "-" would be read
	// as a flag. Real ids include brackets (opus[1m]) and slashes
	// (openrouter/…).
	modelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,127}$`)
)

// ModelInfo is one servable model.
type ModelInfo struct {
	ID        string // "<runtime>/<model>", the id clients use
	Runtime   string
	Model     string // the runtime's model id; "default" means no --model
	Label     string
	Class     Class
	Validated bool     // the roster has a recent passing validation
	Efforts   []string // reasoning effort levels the model accepts
	Alias     bool     // another name for an earlier entry: resolvable, not listed
	// ReadAccess says monomind can run the model's runtime read-only (--access
	// read): the handshake has the capability and the runtime's scan entry lists
	// the mode. A leg of a conversation with tools needs it where the runtime's
	// own tools are not gated by monomind.
	ReadAccess bool
	// Sandboxable says monomind can apply to the model's runtime the sandbox that every
	// turn asks for (monomind.TurnSandboxMode): the handshake has agent-exec-sandbox and
	// the runtime's scan entry lists the mode, which is what monomind.SandboxArgs asks
	// of them. A leg of a conversation with tools requires it: under the sandbox
	// monomind lets only the prefixed names of the declared functions through, and
	// without it a function called Bash would open the runtime's own Bash.
	Sandboxable bool
	// What that validation measured, for choosing between models (the auto
	// model's fallback rule): zero when the model is not validated, and HasCost
	// says whether the runtime reported a cost at all.
	CostUSD   float64
	HasCost   bool
	LatencyMs int64
}

// CatalogFuncs are the lookups the catalog is built from. Production wires
// them to monomind and the roster; tests replace them.
type CatalogFuncs struct {
	Scan   func(ctx context.Context) (*monomind.ScanResult, error)
	Models func(ctx context.Context, runtime, binary string) ([]monomind.RuntimeModel, error)
	Caps   func(ctx context.Context) (*monomind.CapabilitySet, error)
	Roster func(ctx context.Context) ([]agentroster.Result, error)
	// Logf receives the catalog's own notices (a failed refresh). nil drops them.
	Logf func(format string, args ...any)
}

// Catalog lists every model of the installed runtimes. The runtimes' own
// listings are fetched in parallel, cached for a TTL, and a load that is
// already running is shared by every caller that arrives meanwhile.
type Catalog struct {
	f   CatalogFuncs
	ttl time.Duration
	now func() time.Time

	mu     sync.Mutex
	cached []ModelInfo
	loaded bool // cached is a real result, possibly an empty list
	at     time.Time
	flight *catalogFlight
	// failStreak counts the refreshes in a row that did not give a good list,
	// which sets how long the next retry waits (retrySoonLocked).
	failStreak int
	// lastLists is each runtime's last listing that succeeded. A runtime
	// whose listing fails at a refresh keeps it, so one bad listing does not
	// turn a model clients use into a 404.
	lastLists map[string][]monomind.RuntimeModel
	// failing marks the runtimes whose last listing failed, so a streak is
	// logged once and not on every retry.
	failing map[string]bool
	// onFirstLoad, when set, is given the first list that loads, once.
	onFirstLoad func([]ModelInfo)
}

type catalogFlight struct {
	done   chan struct{}
	models []ModelInfo
	err    error
	// abandoned: the load was a bound one whose starter left before it ended. What
	// it holds is not a list, and a caller that joined it and is still there starts
	// a load of its own.
	abandoned bool
}

// NewCatalog returns a Catalog that reuses a load for ttl.
func NewCatalog(f CatalogFuncs, ttl time.Duration) *Catalog {
	return &Catalog{f: f, ttl: ttl, now: time.Now}
}

const (
	// loadTimeout bounds one load: a scan plus the slowest runtime listing.
	loadTimeout = 90 * time.Second
	// staleRetryAfter is how soon a failed refresh is tried again while the
	// previous list is served meanwhile.
	staleRetryAfter = 30 * time.Second
)

// listTimeout bounds one runtime's own model listing, so a slow or hung
// runtime costs its models and not the whole list. A variable so a test can
// shorten it.
var listTimeout = 12 * time.Second

var errLoadAborted = errors.New("the model list could not be loaded")

func (c *Catalog) logf(format string, args ...any) {
	if c.f.Logf != nil {
		c.f.Logf(format, args...)
	}
}

// load builds the list. degraded reports that some runtime's own listing
// failed, timed out or panicked and was replaced by its last good one (or, with
// none, by whatever came back).
func (c *Catalog) load(ctx context.Context) (models []ModelInfo, degraded bool, err error) {
	scan, err := c.f.Scan(ctx)
	if err != nil {
		return nil, false, err
	}
	var caps *monomind.CapabilitySet
	if c.f.Caps != nil {
		caps, _ = c.f.Caps(ctx)
	}
	installed := scan.Installed()
	roster := c.rosterResults(ctx)

	c.mu.Lock()
	previous := maps.Clone(c.lastLists)
	c.mu.Unlock()

	lists := make([][]monomind.RuntimeModel, len(installed))
	failed := make([]bool, len(installed))
	var wg sync.WaitGroup
	for i, e := range installed {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// These goroutines are not the caller's: a panic here would end the
			// process, so it costs this runtime its listing and nothing else.
			defer func() {
				if r := recover(); r != nil {
					c.logf("listing the models of %s panicked: %v", e.ID, r)
					failed[i], lists[i] = true, previous[e.ID]
				}
			}()
			bin := ""
			if e.Binary != nil {
				bin = *e.Binary
			}
			lctx, cancel := context.WithTimeout(ctx, listTimeout)
			defer cancel()
			models, err := c.f.Models(lctx, e.ID, bin)
			// monomind.ListModels answers a listing that ran out of time with its
			// built-in fallback and no error, so a timeout counts as a failure
			// like an error does: the fallback must not replace the live list.
			// The strict variant the gateway uses says the same of a listing that
			// failed (ErrBuiltinModels): its list stands in, and is not trusted.
			if err != nil || lctx.Err() != nil {
				failed[i] = true
				if last, ok := previous[e.ID]; ok {
					lists[i] = last
				} else if err == nil || errors.Is(err, monomind.ErrBuiltinModels) {
					lists[i] = models
				} // else the runtime keeps its default model only
				return
			}
			lists[i] = models
		}()
	}
	wg.Wait()
	good := map[string][]monomind.RuntimeModel{}
	var newlyFailing []string
	c.mu.Lock()
	if c.lastLists == nil {
		c.lastLists, c.failing = map[string][]monomind.RuntimeModel{}, map[string]bool{}
	}
	for i, e := range installed {
		if failed[i] {
			degraded = true
			if !c.failing[e.ID] {
				c.failing[e.ID] = true
				newlyFailing = append(newlyFailing, e.ID)
			}
		} else {
			good[e.ID] = lists[i]
			delete(c.failing, e.ID)
		}
	}
	maps.Copy(c.lastLists, good)
	c.mu.Unlock()
	for _, id := range newlyFailing {
		c.logf("the model list of %s could not be refreshed: using the last one, and trying again soon", id)
	}

	validated := validatedModels(roster, scan, c.now())
	var out []ModelInfo
	for i, e := range installed {
		class := ClassifyRuntime(e, caps)
		readAccess := caps.Has(monomind.CapAgentExecAccessRead) && slices.Contains(e.AccessModes, monomind.AccessRead)
		var modes []string // as Exec reads them: only from a monomind that has --sandbox
		if caps.Has(monomind.CapAgentExecSandbox) {
			modes = e.SandboxModes
		}
		_, sandboxStatus := monomind.SandboxArgs(caps, modes, e.ID, monomind.TurnSandboxMode)
		sandboxable := sandboxStatus == monomind.SandboxStatusSandboxed && !e.RuleBasedSandbox(monomind.TurnSandboxMode)
		seen := map[string]bool{}
		add := func(m monomind.RuntimeModel) {
			if seen[m.ID] || !modelRE.MatchString(m.ID) {
				return
			}
			seen[m.ID] = true
			label := m.Label
			if label == "" {
				label = m.ID
			}
			id := e.ID + "/" + m.ID
			v, isValidated := validated[id]
			out = append(out, ModelInfo{
				ID: id, Runtime: e.ID, Model: m.ID, Label: label, Class: class,
				Validated: isValidated, Efforts: m.EffortLevels, Alias: m.AliasOf != "",
				CostUSD: v.CostUSD, HasCost: v.HasCost, LatencyMs: v.LatencyMs, ReadAccess: readAccess, Sandboxable: sandboxable,
			})
		}
		listsDefault := false
		for _, m := range lists[i] {
			if m.ID == agentroster.DefaultModel {
				listsDefault = true
			}
		}
		if !listsDefault {
			add(monomind.RuntimeModel{ID: agentroster.DefaultModel, Label: e.ID + " default model"})
		}
		for _, m := range lists[i] {
			add(m)
		}
		// A model the user added to the roster by hand stays usable even when
		// the runtime does not list it.
		for _, r := range roster {
			if r.Runtime == e.ID {
				add(monomind.RuntimeModel{ID: r.Model, Label: r.Label, EffortLevels: r.EffortLevels})
			}
		}
	}
	return out, degraded, nil
}

// rosterResults returns the stored roster rows. The roster is machine-wide
// and may be empty or unreadable, which is not an error here.
func (c *Catalog) rosterResults(ctx context.Context) []agentroster.Result {
	if c.f.Roster == nil {
		return nil
	}
	results, err := c.f.Roster(ctx)
	if err != nil {
		return nil
	}
	return results
}

// validation is what the latest passing validation of a model measured.
type validation struct {
	CostUSD   float64
	HasCost   bool
	LatencyMs int64
}

// validatedModels returns, by "<runtime>/<model>" id, the models with a recent
// passing validation and what it measured.
func validatedModels(results []agentroster.Result, scan *monomind.ScanResult, now time.Time) map[string]validation {
	ready := map[string]validation{}
	for _, rr := range agentroster.Build(results, scan, now, agentroster.DefaultMaxAge) {
		for _, m := range rr.Models {
			if m.State == agentroster.StateReady {
				ready[rr.Runtime+"/"+m.Model] = validation{CostUSD: m.CostUSD, HasCost: m.HasCost, LatencyMs: m.LatencyMs}
			}
		}
	}
	return ready
}

// splitModel parses a client's model name: "<runtime>/<model>", or a bare
// runtime meaning its default model. The runtime is case-insensitive and
// "agy" is accepted for antigravity. Both halves must have the shapes that
// may reach a command line.
func splitModel(name string) (runtime, model string, ok bool) {
	rt, model, found := strings.Cut(name, "/")
	rt = strings.ToLower(rt)
	if alias, isAlias := aliases[rt]; isAlias {
		rt = alias
	}
	if !found {
		model = agentroster.DefaultModel
	}
	if !runtimeRE.MatchString(rt) || !modelRE.MatchString(model) {
		return "", "", false
	}
	return rt, model, true
}

// Resolve finds the catalog entry a client's model name refers to.
func (c *Catalog) Resolve(ctx context.Context, name string) (ModelInfo, error) {
	rt, model, ok := splitModel(name)
	if !ok {
		return ModelInfo{}, ErrUnknownModel
	}
	models, err := c.Models(ctx)
	if err != nil {
		return ModelInfo{}, fmt.Errorf("listing models: %w", err)
	}
	for _, m := range models {
		if m.Runtime == rt && m.Model == model {
			return m, nil
		}
	}
	// A model the list no longer offers answers as the one that replaced it,
	// listed by its id or by its family's alias.
	if _, old := monomind.Superseded(rt, model); old {
		for _, m := range models {
			if m.Runtime == rt && monomind.SameModel(rt, m.Model, model) {
				return m, nil
			}
		}
	}
	return ModelInfo{}, ErrUnknownModel
}

// Visible is the list a client may see: aliases hidden, and only the models
// the policy allows.
func (c *Catalog) Visible(ctx context.Context, p Policy) ([]ModelInfo, error) {
	models, err := c.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		if !m.Alias && p.Allows(m.Class) {
			out = append(out, m)
		}
	}
	return out, nil
}
