package orgdecide

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Client is the monomind surface the service reads and resolves through.
type Client interface {
	Status(ctx context.Context, root, org string) (json.RawMessage, error)
	Approvals(ctx context.Context, root, org string) (json.RawMessage, error)
	Questions(ctx context.Context, root, org string) (json.RawMessage, error)
	Gates(ctx context.Context, root, org string) (json.RawMessage, error)
	Approve(ctx context.Context, root, org, role, action string, approve bool, opts monomind.ResolveOptions) error
	Answer(ctx context.Context, root, org, questionID, answer string, opts monomind.ResolveOptions) error
	Gate(ctx context.Context, root, org, gateID string, approve bool, resolution string, opts monomind.ResolveOptions) error
	// Notify messages a role — how a denied approval carries its reason,
	// which monomind's approve/deny cannot (C-52).
	Notify(ctx context.Context, root, org, role, subject, body string) error
}

// autonomySender is the sender role of decision-service messages, so a
// requester can tell them from another agent's message.
const autonomySender = "autonomy"

// MonomindClient is Client over the monomind proxies.
type MonomindClient struct{}

func (MonomindClient) Status(ctx context.Context, root, org string) (json.RawMessage, error) {
	return monomind.OrgStatus(ctx, root, org)
}
func (MonomindClient) Approvals(ctx context.Context, root, org string) (json.RawMessage, error) {
	return monomind.OrgApprovals(ctx, root, org)
}
func (MonomindClient) Questions(ctx context.Context, root, org string) (json.RawMessage, error) {
	return monomind.OrgQuestions(ctx, root, org)
}
func (MonomindClient) Gates(ctx context.Context, root, org string) (json.RawMessage, error) {
	return monomind.OrgGates(ctx, root, org)
}
func (MonomindClient) Approve(ctx context.Context, root, org, role, action string, approve bool, opts monomind.ResolveOptions) error {
	var err error
	if approve {
		_, err = monomind.OrgApproveWith(ctx, root, org, role, action, opts)
	} else {
		_, err = monomind.OrgDenyWith(ctx, root, org, role, action, opts)
	}
	return err
}
func (MonomindClient) Notify(ctx context.Context, root, org, role, subject, body string) error {
	_, err := monomind.OrgInbox(ctx, root, org, monomind.InboxMessage{
		To: role, From: org + ":" + autonomySender, Subject: subject, Body: body,
	})
	return err
}
func (MonomindClient) Answer(ctx context.Context, root, org, questionID, answer string, opts monomind.ResolveOptions) error {
	_, err := monomind.OrgAnswerWith(ctx, root, org, questionID, answer, opts)
	return err
}
func (MonomindClient) Gate(ctx context.Context, root, org, gateID string, approve bool, resolution string, opts monomind.ResolveOptions) error {
	_, err := monomind.OrgGateResolveWith(ctx, root, org, gateID, approve, resolution, opts)
	return err
}

// ProfileRoot is one profile's org folder.
type ProfileRoot struct {
	ProfileID string
	Root      string
}

// Service routes and resolves decisions.
type Service struct {
	DB     *sql.DB
	Store  *Store
	Client Client
	// NewDecider builds the model decider for an org's settings (tests
	// inject fakes).
	NewDecider func(a *Autonomy) DeciderImpl
	// Roots lists every profile root to serve (the daemon spans profiles).
	Roots func(ctx context.Context) ([]ProfileRoot, error)
	// WorkflowFacts describes a granted workflow for the decider prompt,
	// from the DB (name, node types, outbound nodes).
	WorkflowFacts func(ctx context.Context, profileID, workflowID string) string
	// LoadWorkflow reads a granted workflow as it is now (file first, as
	// execution does), so a grant whose workflow gained an outbound node
	// routes as irreversible before any save or reconcile raises its row.
	// Nil routes by the stored tier alone.
	LoadWorkflow orggrant.WorkflowLoader
	// ListWorkflows lists a profile's workflows as execution does (files
	// first), so a workflow a granted one calls by name is found as a run
	// finds it. Nil leaves such a call unresolved: it counts as sending.
	ListWorkflows func(ctx context.Context, profileID string) ([]workflow.Workflow, error)
	Interval      time.Duration
	Logf          func(format string, args ...interface{})
	now           func() time.Time

	jevWarned sync.Map // profile/org → struct{}: "jev without a key" logged once
}

// NewService wires a Service with production defaults.
func NewService(db *sql.DB, roots func(ctx context.Context) ([]ProfileRoot, error)) *Service {
	s := &Service{
		DB: db, Store: NewStore(db), Client: MonomindClient{}, Roots: roots, Interval: 15 * time.Second,
		Logf: func(string, ...interface{}) {},
		now:  time.Now,
	}
	s.NewDecider = s.defaultDecider
	return s
}

func (s *Service) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// Run ticks until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			s.Logf("orgdecide: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick processes every org that has autonomy settings stored. Orgs without
// a row are manual, so there is nothing for the service to do for them.
func (s *Service) Tick(ctx context.Context) error {
	roots, err := s.Roots(ctx)
	if err != nil {
		return err
	}
	for _, pr := range roots {
		orgs, err := s.Store.ListOrgs(ctx, pr.ProfileID)
		if err != nil {
			return err
		}
		for _, org := range orgs {
			if ctx.Err() != nil {
				return nil
			}
			if _, err := s.ProcessOrg(ctx, pr.ProfileID, pr.Root, org); err != nil {
				s.Logf("orgdecide: %s/%s: %v", pr.ProfileID, org, err)
			}
		}
		s.SweepDelegations(ctx, pr.ProfileID, pr.Root)
	}
	return nil
}

// Pending collects an org's pending items with tiers assigned. The bool
// reports a partial list: at least one source failed, so an item's absence
// here does not mean it is gone.
func (s *Service) Pending(ctx context.Context, profileID, root, org string, a *Autonomy) ([]Item, string, bool, error) {
	var items []Item
	var errs []string
	collect := func(name string, fetch func(context.Context, string, string) (json.RawMessage, error), parse func([]byte) ([]Item, error)) {
		raw, err := fetch(ctx, root, org)
		if err != nil {
			errs = append(errs, name+": "+err.Error())
			return
		}
		got, err := parse(raw)
		if err != nil {
			errs = append(errs, err.Error())
			return
		}
		items = append(items, got...)
	}
	collect("approvals", s.Client.Approvals, ParseApprovals)
	collect("questions", s.Client.Questions, ParseQuestions)
	collect("gates", s.Client.Gates, ParseGates)
	if len(errs) == 3 {
		return nil, "", false, errors.New(strings.Join(errs, "; "))
	}
	// A partial fetch still routes what it did see — an item missed this tick
	// is picked up on the next one — but a caller that reads "absent" as
	// "resolved elsewhere" must know the list is incomplete (SweepDelegations).
	partial := len(errs) > 0
	if s.DB != nil {
		doc, _ := orgdesign.Load(root, org)
		if hil, err := PendingHIL(ctx, s.DB, profileID, org, doc); err != nil {
			partial = true
		} else {
			items = append(items, hil...)
		}
	}

	runID := ""
	if raw, err := s.Client.Status(ctx, root, org); err == nil {
		var st struct {
			Run string `json:"run"`
		}
		_ = json.Unmarshal(raw, &st)
		runID = st.Run
	}

	// One read of each granted workflow per pass serves its tier and
	// whether a call of it may send.
	load := onceEach(s.LoadWorkflow)
	facts := s.tierFactsWith(ctx, profileID, root, org, load)
	for i := range items {
		items[i].Tier = TierFor(items[i].Class, items[i].Requester, a.Tiers, facts)
		if s.DB != nil {
			items[i].Sends = ItemSends(ctx, s.DB, profileID, org, items[i], workflowReads{load, s.ListWorkflows})
		}
	}
	SortItems(items)
	return items, runID, partial, nil
}

func (s *Service) tierFacts(ctx context.Context, profileID, root, org string) TierFacts {
	return s.tierFactsWith(ctx, profileID, root, org, s.LoadWorkflow)
}

// onceEach is load reading each workflow once: later reads of the same id
// get the first read's result. Nil stays nil.
func onceEach(load orggrant.WorkflowLoader) orggrant.WorkflowLoader {
	if load == nil {
		return nil
	}
	type read struct {
		wf  *workflow.Workflow
		err error
	}
	seen := map[string]read{}
	return func(ctx context.Context, id string) (*workflow.Workflow, error) {
		if r, ok := seen[id]; ok {
			return r.wf, r.err
		}
		wf, err := load(ctx, id)
		seen[id] = read{wf, err}
		return wf, err
	}
}

func (s *Service) tierFactsWith(ctx context.Context, profileID, root, org string, load orggrant.WorkflowLoader) TierFacts {
	grants, _ := orggrant.NewStore(s.DB).ListGrants(ctx, profileID, org, "")
	doc, _ := orgdesign.Load(root, org)
	tiers := map[string]string{} // alias → tier, for this pass
	return TierFacts{
		GrantTier: func(alias string) string {
			if tier, ok := tiers[alias]; ok {
				return tier
			}
			tier := ""
			for _, g := range grants {
				if t := g.Automation(); t != nil && t.Alias == alias {
					tier = s.currentGrantTier(ctx, g, load)
					break
				}
			}
			tiers[alias] = tier
			return tier
		},
		RoleHasGrantsAndBash: func(role string) bool {
			held := false
			for _, g := range grants {
				if g.RoleID == role {
					held = true
				}
			}
			if !held || doc == nil {
				return false
			}
			r, _ := doc.FindRole(role)
			if r == nil {
				return false
			}
			for _, d := range r.PolicyStrings("denyTools") {
				if d == "Bash" {
					return false
				}
			}
			return true
		},
	}
}

// currentGrantTier is g's stored tier, raised to irreversible when its
// workflow now has an outbound node (#284). The raise is also stored, so the
// display copy catches up on the next reconcile. A workflow that cannot be
// read leaves the stored tier in force.
func (s *Service) currentGrantTier(ctx context.Context, g orggrant.Grant, load orggrant.WorkflowLoader) string {
	t := g.Automation()
	if load == nil || t.Tier == orgdesign.TierIrreversible || !orgdesign.ValidTier(t.Tier) {
		return t.Tier
	}
	wf, err := load(ctx, t.WorkflowID)
	if err != nil || wf == nil || len(orggrant.OutboundNodes(wf)) == 0 {
		return t.Tier
	}
	raised, err := orggrant.NewStore(s.DB).RaiseTiers(ctx, []orggrant.Grant{g},
		func(context.Context, string) (*workflow.Workflow, error) { return wf, nil })
	if err != nil {
		s.Logf("orgdecide: %s/%s: storing the raised tier of %q: %v", g.ProfileID, g.OrgName, t.Alias, err)
	}
	for _, r := range raised {
		s.Logf("orgdecide: %s/%s: %s", r.Profile, r.Org, r)
	}
	return orgdesign.TierIrreversible
}

// ProcessOrg routes and resolves every pending item of one org once. An
// item is routed again only when the org's effective level changed since
// its last routing (a pause, a resume, or an operator raising the level).
//
// Only one process processes an org at a time (the CLI's `org process` and
// the daemon's tick can run together): another process's claim on the org
// leaves this pass with nothing to decide, so a paid decision is not made
// twice and monomind is not answered twice.
func (s *Service) ProcessOrg(ctx context.Context, profileID, root, org string) ([]Decision, error) {
	release, err := daemonhb.LockFile(processLockPath(root, org))
	if errors.Is(err, daemonhb.ErrHeld) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("orgdecide: claim %s: %w", org, err)
	}
	defer release()
	a, err := s.Store.Get(ctx, profileID, org)
	if err != nil {
		return nil, err
	}
	level := a.EffectiveLevel(s.clock())
	items, runID, _, err := s.Pending(ctx, profileID, root, org, a)
	if err != nil {
		return nil, err
	}
	doc, _ := orgdesign.Load(root, org)
	var out []Decision
	for _, it := range items {
		latest, err := s.Store.LatestFor(ctx, profileID, org, it.Kind, it.Ref)
		if err != nil {
			return out, err
		}
		if latest != nil && latest.Level == level {
			continue
		}
		d, err := s.decide(ctx, profileID, root, org, runID, level, a, doc, it)
		if err != nil {
			s.Logf("orgdecide: %s %s %s: %v", org, it.Kind, it.Ref, err)
		}
		if d != nil {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (s *Service) decide(ctx context.Context, profileID, root, org, runID, level string, a *Autonomy, doc *orgdesign.Doc, it Item) (*Decision, error) {
	base := Decision{
		ProfileID: profileID, OrgName: org, RunID: runID, ItemKind: it.Kind, ItemRef: it.Ref, ItemHash: it.Hash,
		Requester: it.Requester, Class: it.Class, Tier: it.Tier, Level: level,
	}
	record := func(resolver, verdict, answer, rationale string, cost *float64, latency *int64) (*Decision, error) {
		d := base
		d.Resolver, d.Verdict, d.AnswerText, d.Rationale, d.CostUSD, d.LatencyMS = resolver, verdict, answer, rationale, cost, latency
		if base.Rationale != "" {
			d.Rationale = base.Rationale + "; " + rationale
		}
		return &d, s.Store.Record(ctx, &d)
	}

	// Repeat-denial rule, at every level (plan §7.7).
	if it.Kind != KindQuestion {
		if n, err := s.Store.DeniedCount(ctx, profileID, org, runID, it.Hash); err == nil && n >= 3 {
			reason := "repeat of a request denied 3 times in this run"
			if err := s.apply(ctx, root, org, it, false, "", "rule", reason); err != nil {
				return nil, err
			}
			return record("rule", VerdictDenied, "", reason, nil, nil)
		}
	}

	route := RouteItem(level, it)
	if route == RouteRule && it.Kind == KindQuestion {
		route = RouteDecider // a rule cannot write an answer
	}
	switch route {
	case RouteHuman:
		if it.Sends {
			return s.toPerson(ctx, profileID, org, it)(record("human", VerdictEscalated, "", "approving it lets a message to a person go out, which only a person decides", nil, nil))
		}
		return s.toPerson(ctx, profileID, org, it)(record("human", VerdictEscalated, "", fmt.Sprintf("%s routes %s items to a person", level, it.Tier), nil, nil))
	case RouteRule:
		reason := fmt.Sprintf("%s items are approved by rule at %s", it.Tier, level)
		if err := s.apply(ctx, root, org, it, true, "", "rule", reason); err != nil {
			return nil, err
		}
		return record("rule", VerdictApproved, "", reason, nil, nil)
	}

	// Decider route. boss and parent hand the item to a role; model and
	// jev decide here.
	if a.Decider.Kind != orgdesign.DeciderModel && a.Decider.Kind != orgdesign.DeciderJev {
		d, reason, err := s.delegate(ctx, profileID, root, org, level, a, doc, it, record)
		if d != nil || err != nil {
			return d, err
		}
		base.Rationale = reason + "; used fallback " + a.Decider.Fallback
	}
	usage, err := s.Store.Usage(ctx, profileID, org, runID)
	if err != nil {
		return nil, err
	}
	if usage.DeciderDecisions >= a.Limits.MaxDecisionsPerRun || usage.DeciderUSD >= a.Limits.MaxDeciderUSDPerRun {
		return s.fail(ctx, root, org, level, a, it, record, "model:"+a.Decider.Model,
			fmt.Sprintf("decider limit for this run reached (%d decisions, $%.2f)", usage.DeciderDecisions, usage.DeciderUSD), nil, nil)
	}

	in := PromptInput{OrgName: org, Item: it, Level: level, OperatorPolicy: a.Policy,
		BudgetNote: fmt.Sprintf("decider has used %d of %d decisions and $%.2f of $%.2f this run", usage.DeciderDecisions, a.Limits.MaxDecisionsPerRun, usage.DeciderUSD, a.Limits.MaxDeciderUSDPerRun)}
	if doc != nil {
		in.OrgGoal = doc.Goal
		if r, _ := doc.FindRole(it.Requester); r != nil {
			in.RequesterTitle, in.Responsibilities = r.Title, r.Responsibilities
		}
	}
	if strings.HasPrefix(it.Class, "grant:") && s.WorkflowFacts != nil {
		alias := strings.TrimPrefix(it.Class, "grant:")
		if doc != nil {
			if ref := doc.FindAutomation(alias); ref != nil {
				in.GrantFacts = s.WorkflowFacts(ctx, profileID, ref.WorkflowID)
			}
		}
	}
	if prior, err := s.Store.List(ctx, profileID, org, DecisionFilter{RunID: runID, Limit: 20}); err == nil {
		for _, p := range prior {
			in.PriorDecisions = append(in.PriorDecisions, fmt.Sprintf("%s %s by %s: %s", p.Class, p.Verdict, p.Resolver, p.Rationale))
		}
	}

	decider := s.NewDecider(a)
	dctx, cancel := context.WithTimeout(ctx, time.Duration(a.Decider.TimeoutSeconds)*time.Second+10*time.Second)
	outcome, derr := decider.Decide(dctx, BuildPrompt(in))
	cancel()
	base.Confidence, base.Probabilities = outcome.Confidence, outcome.Probabilities
	cost := outcome.CostUSD
	lat := outcome.Latency.Milliseconds()
	resolver := outcome.Resolver
	if resolver == "" {
		resolver = "model:" + a.Decider.Model
	}
	if derr != nil {
		return s.fail(ctx, root, org, level, a, it, record, resolver, derr.Error(), &cost, &lat)
	}
	v := outcome.Verdict
	switch v.Verdict {
	case "escalate":
		return s.toPerson(ctx, profileID, org, it)(record(resolver, VerdictEscalated, "", v.Rationale, &cost, &lat))
	case "answer":
		if err := s.apply(ctx, root, org, it, true, v.Answer, resolver, v.Rationale); err != nil {
			return nil, err
		}
		return record(resolver, VerdictAnswered, v.Answer, v.Rationale, &cost, &lat)
	case "approve", "deny":
		approve := v.Verdict == "approve"
		if err := s.apply(ctx, root, org, it, approve, "", resolver, v.Rationale); err != nil {
			return nil, err
		}
		verdict := VerdictDenied
		if approve {
			verdict = VerdictApproved
		}
		return record(resolver, verdict, "", v.Rationale, &cost, &lat)
	}
	return s.fail(ctx, root, org, level, a, it, record, resolver, "unexpected verdict "+v.Verdict, &cost, &lat)
}
