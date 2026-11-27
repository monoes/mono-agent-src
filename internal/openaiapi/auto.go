package openaiapi

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/monoes/mono-agent/internal/agentroster"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// autoPromptRunes is how much of the last user message Jev is sent: the first
// 4,000 characters, which the api_auto surface's egress list says.
const autoPromptRunes = 4000

// AutoStatus says whether the auto model works for a profile, and what is
// missing when it does not.
type AutoStatus struct {
	Available bool
	// Missing names what to set up when it is not available.
	Missing string
	// KeySource says where the Jev key is (jevconf's "vault" or "env") when it is
	// available: a key from the environment is the environment of the process that
	// asks, which a CLI's is not a server's.
	KeySource string
}

// AutoFuncs are what the auto model needs from outside the gateway: the zero
// value means auto is never available. Production wires them to Jev
// (DefaultAuto); tests replace them.
type AutoFuncs struct {
	// Status reports whether profileID can use auto. It decrypts nothing: a
	// listing must never trigger a keyring prompt.
	Status func(ctx context.Context, profileID string) AutoStatus
	// Choose asks Jev which of the options (id to description) suits the prompt
	// best, and how sure it is: the top option's probability, and what the
	// threshold may be lowered to for a sampled pick (jevconf.SampledCeiling;
	// 0: not lowered). It runs only after Status said yes.
	Choose func(ctx context.Context, profileID, prompt string, options map[string]string) (id string, p, ceiling float64, err error)
	// Threshold is the lowest probability of Jev's pick the profile accepts
	// (pickAuto lowers it to the pick's ceiling).
	Threshold func(profileID string) float64
}

// autoStatus is what the profile may do with auto.
func (g *Gateway) autoStatus(ctx context.Context, profileID string) AutoStatus {
	if g.deps.Auto.Status == nil || g.deps.Auto.Choose == nil {
		return AutoStatus{Missing: "automatic model choice, which this server was not set up with"}
	}
	return g.deps.Auto.Status(ctx, profileID)
}

// What a request needs of the model that serves it, for the models auto may pick.
const (
	capText  = "text" // every model has it: chat works in every class
	capImage = "image"
	capTools = "tools"
)

// autoCandidates returns the models auto may pick among for a chat request from a
// key whose effective policy is eff: see autoCandidatesFor.
func (g *Gateway) autoCandidates(ctx context.Context, pr Principal, eff Policy) ([]ModelInfo, *apiError) {
	return g.autoCandidatesFor(ctx, pr, eff, capText)
}

// autoCandidatesFor returns the models auto may pick among, for a request that
// needs capability, from a key whose effective policy is eff: the ones that policy
// allows, within what the operator let auto pick (chat-only unless they raised it),
// that have the capability. Or the 404 that says why auto is not available: Jev has
// no key or the surface is off for the profile, or there is no such model.
func (g *Gateway) autoCandidatesFor(ctx context.Context, pr Principal, eff Policy, capability string) ([]ModelInfo, *apiError) {
	if st := g.autoStatus(ctx, pr.ProfileID); !st.Available {
		return nil, errAutoUnavailable(st.Missing)
	}
	models, err := g.catalog.Visible(ctx, eff.ForAuto())
	if err != nil {
		return nil, catalogError(err)
	}
	if models = g.withCapability(models, capability); len(models) == 0 {
		return nil, errAutoUnavailable(g.noAutoCandidates(ctx, eff, capability))
	}
	return models, nil
}

// withCapability keeps the models that have the capability.
func (g *Gateway) withCapability(models []ModelInfo, capability string) []ModelInfo {
	var has func(ModelInfo) bool
	switch capability {
	case capImage:
		has = g.cfg.CanMakeImages
	case capTools:
		has = g.cfg.ServesTools
	default:
		return models
	}
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		if has(m) {
			out = append(out, m)
		}
	}
	return out
}

// noAutoCandidates says what is missing when auto has nothing to pick among for a
// request that needs capability, for a key whose effective policy is eff.
func (g *Gateway) noAutoCandidates(ctx context.Context, eff Policy, capability string) string {
	if capability == capTools {
		if g.cfg.ToolsOff() {
			return "a model that calls tools, and tool calling is " + toolsOffBy
		}
		if usable, err := g.catalog.Visible(ctx, eff); err == nil && len(g.withCapability(usable, capTools)) > 0 {
			return fmt.Sprintf("a model that calls tools within what auto may pick (%s here): the models this key may use that call tools run as sandboxed or unconfined, "+
				"and the operator can raise --auto-confinement (MONOAGENT_API_AUTO_CONFINEMENT) to sandboxed or any", eff.ForAuto())
		}
		return fmt.Sprintf("a model this key may use that calls tools, and there is none: tool calling is served on %s only (MONOAGENT_API_TOOL_RUNTIMES), %s",
			strings.Join(g.cfg.ToolRuntimeList(), ", "), g.whyNoToolModel(ctx, eff))
	}
	if capability != capImage {
		return "at least one model the server's confinement policy allows auto to pick (chat-only, unless --auto-confinement says more)"
	}
	if usable, err := g.catalog.Visible(ctx, eff); err == nil && len(g.withCapability(usable, capImage)) > 0 {
		return fmt.Sprintf("an image model within what auto may pick (%s here): the image models this key may use run as sandboxed or unconfined, "+
			"and the operator can raise --auto-confinement (MONOAGENT_API_AUTO_CONFINEMENT) to sandboxed or any", eff.ForAuto())
	}
	return fmt.Sprintf("an image model this key may use, and there is none: no installed runtime of the image list (%s) can make images under "+
		"the server's confinement policy (--confinement, and --context-confinement for a key created with --context)", strings.Join(g.cfg.ImageRuntimeList(), ", "))
}

// whyNoToolModel says why the models of the runtimes that serve tool calling cannot serve it
// here, for a key whose effective policy is eff: the causes are the ones the models of those
// runtimes this key may use are refused for (Config.toolsRefusal), each with the runtimes it
// is true of, and neither is cured by --auto-confinement. With no model of those runtimes
// to use at all, it says that.
func (g *Gateway) whyNoToolModel(ctx context.Context, eff Policy) string {
	list := g.cfg.ToolRuntimeList()
	var noSandbox, noRead []string
	if usable, err := g.catalog.Visible(ctx, eff); err == nil {
		for _, m := range usable {
			if !slices.Contains(list, m.Runtime) {
				continue
			}
			if !m.Sandboxable && !slices.Contains(noSandbox, m.Runtime) {
				noSandbox = append(noSandbox, m.Runtime)
			}
			if m.Class != ChatOnly && !m.ReadAccess && !slices.Contains(noRead, m.Runtime) {
				noRead = append(noRead, m.Runtime)
			}
		}
	}
	var causes []string
	if len(noSandbox) > 0 {
		causes = append(causes, fmt.Sprintf("a turn with tools requires monomind's sandbox, which this machine's monomind cannot apply to %s", strings.Join(noSandbox, ", ")))
	}
	if len(noRead) > 0 {
		causes = append(causes, fmt.Sprintf("monomind cannot run %s read-only, which a runtime that is not chat-only needs", strings.Join(noRead, ", ")))
	}
	if len(causes) == 0 {
		return "and no model of those runtimes is available to this key (the runtime is not installed, or the policy does not allow it)"
	}
	return "and " + strings.Join(causes, ", and ")
}

// autoObject is auto as a model of the list. Its confinement is the strongest
// class it picks within: the key's policy, capped by what the operator let auto
// pick. What Jev picks is never above it. visible are the models the key may use,
// and auto says it makes images, or calls tools, only when one of those it may
// pick does.
func (g *Gateway) autoObject(eff Policy, visible []ModelInfo) modelObject {
	capabilities := []string{capText}
	within := autoWithin(visible, eff.ForAuto())
	for _, capability := range []string{capImage, capTools} {
		if len(g.withCapability(within, capability)) > 0 {
			capabilities = append(capabilities, capability)
		}
	}
	return modelObject{
		ID: autoModelID, Object: "model", OwnedBy: "jev",
		Monoagent: modelMeta{
			Runtime: autoModelID, Model: autoModelID, Label: "Jev picks the model for each request",
			Confinement: eff.ForAuto().Max.String(), Capabilities: capabilities,
		},
	}
}

// autoWithin keeps the models a policy allows.
func autoWithin(models []ModelInfo, p Policy) []ModelInfo {
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		if p.Allows(m.Class) {
			out = append(out, m)
		}
	}
	return out
}

// autoPick is the model auto chose, and by what.
type autoPick struct {
	Model ModelInfo
	// By is "jev" when Jev's answer was used, "rule" when the rule decided.
	By string
}

// pickAuto chooses among candidates (at least one, all of them allowed by the
// key's policy: Jev only picks among what the code lists). It asks Jev unless
// there is nothing to choose between, and uses the rule when Jev does not
// answer, in time, with one of the candidates, sure enough. It never fails: the
// request has a model whatever happens at TypeSafe.
func (g *Gateway) pickAuto(ctx context.Context, profileID, prompt string, candidates []ModelInfo) autoPick {
	if len(candidates) == 1 {
		return autoPick{Model: candidates[0], By: "rule"}
	}
	breaker := g.autoBreakerFor(profileID)
	if !breaker.allow() { // Jev has not been answering: the rule picks, without waiting for it
		return autoPick{Model: ruleChoice(candidates), By: "rule"}
	}
	options := make(map[string]string, len(candidates))
	for _, m := range candidates {
		options[m.ID] = autoDescription(m)
	}
	cctx, cancel := context.WithTimeout(ctx, g.cfg.AutoTimeout)
	defer cancel()
	id, p, ceiling, err := g.ask(cctx, profileID, clipRunes(prompt, autoPromptRunes), options)
	if ctx.Err() != nil { // the caller left: nothing was decided, and Jev is not to blame
		breaker.abandon()
		return autoPick{Model: ruleChoice(candidates), By: "rule"}
	}
	noProbability := errors.Is(err, errNoProbability)
	switch breaker.record(err == nil || noProbability) {
	case breakerOpened:
		g.deps.Logf("auto: Jev gave no answer %d times in a row for profile %s: stop asking it for %s, the rule decides meanwhile", breaker.threshold, profileID, breaker.cooldown)
	case breakerClosed:
		g.deps.Logf("auto: Jev answers again for profile %s", profileID)
	}
	reason := ""
	switch {
	case noProbability:
		reason = "no probability"
	case err != nil:
		reason = "no answer"
	case g.deps.Auto.Threshold != nil && p < jevconf.CapThreshold(g.deps.Auto.Threshold(profileID), ceiling):
		reason = "not sure enough"
	default:
		if i := slices.IndexFunc(candidates, func(m ModelInfo) bool { return m.ID == id }); i >= 0 {
			return autoPick{Model: candidates[i], By: "jev"}
		}
		reason = "an answer that is not one of the options"
	}
	g.deps.Logf("auto: Jev did not decide for profile %s (%s): the rule picks", profileID, reason)
	return autoPick{Model: ruleChoice(candidates), By: "rule"}
}

// ask puts the question to Jev and gives up when ctx ends, even when Choose does
// not look at ctx: resolving the profile's Jev key can wait on a keyring, and the
// request holds a slot meanwhile. An abandoned question finishes in its own
// goroutine, which is why a panic in it is turned into an error here: nothing
// else would recover it, and it would take the server down.
func (g *Gateway) ask(ctx context.Context, profileID, prompt string, options map[string]string) (string, float64, float64, error) {
	type answer struct {
		id         string
		p, ceiling float64
		err        error
	}
	done := make(chan answer, 1)
	go func() {
		defer func() {
			if recover() != nil { // what it says is dropped: it could quote the prompt
				done <- answer{err: errors.New("the question panicked")}
			}
		}()
		id, p, ceiling, err := g.deps.Auto.Choose(ctx, profileID, prompt, options)
		done <- answer{id, p, ceiling, err}
	}()
	select {
	case a := <-done:
		return a.id, a.p, a.ceiling, a.err
	case <-ctx.Done():
		return "", 0, 0, ctx.Err()
	}
}

// ruleChoice is the model auto uses when Jev does not decide: of the models with
// a recent passing validation, the most confined, then the cheapest, then the
// fastest (a cost or a latency nobody measured sorts last); with none, the default
// model of a runtime; with none of those, the first candidate. The class comes
// first everywhere, so a TypeSafe outage never moves a request to a less confined
// model for being cheaper; within a class, runtimes are taken in the order claude,
// codex, antigravity, then the rest alphabetically. candidates must not be empty.
func ruleChoice(candidates []ModelInfo) ModelInfo {
	byClass := func(a, b ModelInfo) int { return cmp.Compare(a.Class, b.Class) }
	byRuntime := func(a, b ModelInfo) int {
		if c := cmp.Compare(runtimeRank(a.Runtime), runtimeRank(b.Runtime)); c != 0 {
			return c
		}
		return cmp.Or(cmp.Compare(a.Runtime, b.Runtime), cmp.Compare(a.ID, b.ID))
	}
	var validated, defaults []ModelInfo
	for _, m := range candidates {
		if m.Validated {
			validated = append(validated, m)
		}
		if m.Model == agentroster.DefaultModel {
			defaults = append(defaults, m)
		}
	}
	switch {
	case len(validated) > 0:
		slices.SortStableFunc(validated, func(a, b ModelInfo) int {
			return cmp.Or(byClass(a, b),
				compareKnownFirst(a.CostUSD, a.HasCost, b.CostUSD, b.HasCost),
				compareKnownFirst(float64(a.LatencyMs), a.LatencyMs > 0, float64(b.LatencyMs), b.LatencyMs > 0),
				byRuntime(a, b))
		})
		return validated[0]
	case len(defaults) > 0:
		slices.SortFunc(defaults, func(a, b ModelInfo) int { return cmp.Or(byClass(a, b), byRuntime(a, b)) })
		return defaults[0]
	}
	sorted := slices.Clone(candidates)
	slices.SortFunc(sorted, func(a, b ModelInfo) int { return cmp.Or(byClass(a, b), byRuntime(a, b)) })
	return sorted[0]
}

// compareKnownFirst orders by value, with a value nobody measured after every one
// somebody did.
func compareKnownFirst(a float64, aKnown bool, b float64, bKnown bool) int {
	switch {
	case aKnown && bKnown:
		return cmp.Compare(a, b)
	case aKnown:
		return -1
	case bKnown:
		return 1
	}
	return 0
}

// runtimeRank is where a runtime stands in the rule's order: claude, codex and
// antigravity first, in that order, every other runtime after them.
func runtimeRank(runtime string) int {
	switch runtime {
	case "claude":
		return 0
	case "codex":
		return 1
	case "antigravity":
		return 2
	}
	return 3
}

// autoDescription is what Jev is told about a model: its label, who runs it, and
// what the latest validation measured.
func autoDescription(m ModelInfo) string {
	label := m.Label
	if label == "" {
		label = m.ID
	}
	parts := []string{fmt.Sprintf("%s (%s)", label, m.Runtime)}
	if m.Validated {
		if m.HasCost && m.CostUSD > 0 {
			parts = append(parts, fmt.Sprintf("test turn cost $%.4f", m.CostUSD))
		}
		if m.LatencyMs > 0 {
			parts = append(parts, fmt.Sprintf("answers in %.1fs", float64(m.LatencyMs)/1000))
		}
	}
	return strings.Join(parts, ", ")
}
