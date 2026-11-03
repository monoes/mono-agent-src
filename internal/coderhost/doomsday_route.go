package coderhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/tasks"
)

// B2 routing of the open-ended mode (spec 6, note 13.4 4B): what the boss may answer by itself, inside the
// charter's per-ask limits. The mode is off by default: with no route installed (SetDoomsdayRoute) these methods
// refuse and no other path of the host changes. Anything over a limit stays a person item, exactly as today.

// ErrDoomsdayOff: no route is installed, the mode is not running on this host.
var ErrDoomsdayOff = fmt.Errorf("%w: the open-ended mode is not running", ErrNotYours)

// ErrParked: the request was over a limit or not lifted; it stays a person item and nothing was answered.
var ErrParked = fmt.Errorf("%w: parked as a person item", ErrLimit)

// ErrDoomsdayNotWired: facts that were asked for without the verified boss they are read for (plan.approve).
// Refused, never approved on guessed numbers.
var ErrDoomsdayNotWired = errors.New("coder host: that open-ended operation is not wired to a source of facts")

// ClassAskCritical is the answer class of a critical upward ask for ops.AskAnswer.Classes.
const ClassAskCritical = ClassAncestorQuestion

// DoomsdayRoute is the enforcement function of the typed operations plus the way to number an approval.
type DoomsdayRoute struct {
	Enforcer      *ops.Enforcer
	NewApprovalID func() string // nil: 16 random bytes in hex
}

// SpendBounds are the bounds of a single grant the host already enforces, for ops.Enforcer.Bounds.
func SpendBounds() ops.SpendBounds {
	min, _ := amountMicro(MinBudgetRequestUSD)
	max, _ := amountMicro(MaxBudgetRequestUSD)
	return ops.SpendBounds{MinMicro: min, MaxMicro: max}
}

// NewDoomsdayFacts reads the facts of a plan, an ask and a budget request from this store.
func (s *Store) NewDoomsdayFacts() ops.Facts { return routeFacts{s} }

// SetDoomsdayRoute installs (or with nil removes) the route.
func (s *Store) SetDoomsdayRoute(r *DoomsdayRoute) { s.doomsday = r }

func (r *DoomsdayRoute) approvalID() string {
	if r.NewApprovalID != nil {
		return r.NewApprovalID()
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// request fills the charter binding from the live charter; the caller is the verified lead's session.
func (r *DoomsdayRoute) request(ctx context.Context, profileID, op string, session int64) (ops.Request, error) {
	act, ok, err := r.Enforcer.Charters.Active(ctx, profileID)
	if err != nil {
		return ops.Request{}, err
	}
	if !ok {
		return ops.Request{}, ops.ErrNoCharter
	}
	return ops.Request{Profile: profileID, CharterID: act.ID, Hash: act.Hash, Generation: act.Generation, Op: op,
		ApprovalID: r.approvalID(), Caller: ops.Caller{Session: session}, Addressed: session}, nil
}

// BossAnswerAsk is the boss's answer to a critical ask addressed to it (ask.answer). sc must be the LeadScope of
// the ancestor the ask names, from its verified token; OperatorScope is not accepted. Over a limit, or for a class
// the charter did not lift, it returns ErrParked and the ask stays open for the person.
func (s *Store) BossAnswerAsk(ctx context.Context, sc CallerScope, profileID string, askID int64, class, answer string) (Ask, Command, error) {
	r := s.doomsday
	if r == nil {
		return Ask{}, Command{}, ErrDoomsdayOff
	}
	sid, err := s.LeadMission(sc, profileID)
	if err != nil {
		return Ask{}, Command{}, err
	}
	a, err := s.GetAsk(profileID, askID)
	if err != nil {
		return Ask{}, Command{}, err
	}
	if a.Importance != AskCritical || a.Status != askOpen || a.AncestorSession != sid {
		return a, Command{}, fmt.Errorf("%w: that is not an open critical ask addressed to you", ErrNotOpen)
	}
	if answer == "" || len(answer) > MaxAnswerBytes {
		return a, Command{}, fmt.Errorf("%w: an answer is 1 to %d bytes", ErrInvalid, MaxAnswerBytes)
	}
	req, err := r.request(ctx, profileID, ops.OpAskAnswer, sid)
	if err != nil {
		return a, Command{}, err
	}
	req.Answer = &ops.AnswerArgs{AskID: askID, Class: class}
	d, err := r.Enforcer.Decide(ctx, req)
	if err != nil {
		return a, Command{}, err
	}
	if d.Verdict != ops.Approved {
		return a, Command{}, fmt.Errorf("%w (%v)", ErrParked, d.Exceeded)
	}
	cmd, err := s.closeAndDeliver(profileID, askID, a.AskerConv, a.Question, "the boss (auto-approved inside the charter)", answer, "boss", askFromLead, nil)
	if err != nil {
		return a, cmd, err
	}
	a.Status = askAnswered
	return a, cmd, nil
}

// BossGrantBudget is the boss's grant of an open budget request (spend.grant), in integer micro-dollars. The
// caller must be a proper ancestor of the requesting session. The grant itself keeps every carve and ancestor
// limit of GrantBudget; a refusal there leaves the request open (the approval is spent, the safe direction).
func (s *Store) BossGrantBudget(ctx context.Context, sc CallerScope, profileID string, requestID, amountMicro int64) (BudgetRequest, error) {
	r := s.doomsday
	if r == nil {
		return BudgetRequest{}, ErrDoomsdayOff
	}
	sid, err := s.LeadMission(sc, profileID)
	if err != nil {
		return BudgetRequest{}, err
	}
	b, err := s.GetBudgetRequest(profileID, requestID)
	if err != nil {
		return BudgetRequest{}, err
	}
	anc, err := s.ancestorsOf(profileID, b.SessionID)
	if err != nil {
		return BudgetRequest{}, err
	}
	if !slices.ContainsFunc(anc, func(n treeNode) bool { return n.id == sid }) {
		return BudgetRequest{}, fmt.Errorf("%w: you are not above the session that asked", ErrNotYours)
	}
	// The grant's own host limits are read first, so an approval is not spent on a grant the host would refuse.
	// (They are read again inside the grant's transaction; a change in between still spends the approval, the safe
	// direction: it is never un-spent, so the boss must ask again.)
	if reason, err := s.budgetRaiseRefusal(s.db, b, fromMicro(amountMicro)); err != nil {
		return BudgetRequest{}, err
	} else if reason != "" {
		return BudgetRequest{}, fmt.Errorf("%w: %s", ErrLimit, reason)
	}
	req, err := r.request(ctx, profileID, ops.OpSpendGrant, sid)
	if err != nil {
		return BudgetRequest{}, err
	}
	req.Grant = &ops.GrantArgs{RequestID: requestID, Target: b.Target, AmountMicro: amountMicro}
	d, err := r.Enforcer.Decide(ctx, req)
	if err != nil {
		return BudgetRequest{}, err
	}
	if d.Verdict != ops.Approved {
		return BudgetRequest{}, fmt.Errorf("%w (%v)", ErrParked, d.Exceeded)
	}
	return s.grantBudget(profileID, requestID, fromMicro(amountMicro))
}

type routeFacts struct{ s *Store }

// Plan reads the facts of a draft's plan as the verified lead of its parent (the boss): the actor travels in ctx
// (BossApprovePlan sets it), and a call without one is refused, never answered on guessed numbers.
func (f routeFacts) Plan(ctx context.Context, profileID string, session int64) (ops.Subject, error) {
	actor, ok := ctx.Value(bossActorKey{}).(tasks.Actor)
	if !ok {
		return ops.Subject{}, ErrDoomsdayNotWired
	}
	pf, err := tasks.NewStore(f.s.db).PlanFactsForBoss(ctx, profileID, session, actor)
	if err != nil {
		return ops.Subject{}, err
	}
	return ops.Subject{Open: pf.Open, Rev: pf.Rev, Digest: pf.Digest, Tasks: pf.Tasks, SpendMicro: pf.SpendMicro,
		Access: pf.AccessLevel, Depth: pf.Depth}, nil
}

// Ask: an answer commits nothing, so its numbers are the least possible; only the class and openness matter.
func (f routeFacts) Ask(_ context.Context, profileID string, askID int64) (ops.Subject, error) {
	a, err := f.s.GetAsk(profileID, askID)
	if err != nil {
		return ops.Subject{}, err
	}
	return ops.Subject{Open: a.Importance == AskCritical && a.Status == askOpen, Class: ClassAskCritical, Access: "scoped", Depth: 1}, nil
}

func (f routeFacts) Grant(_ context.Context, profileID string, requestID int64) (ops.GrantFacts, error) {
	b, err := f.s.GetBudgetRequest(profileID, requestID)
	if err != nil {
		return ops.GrantFacts{}, err
	}
	m, ok := amountMicro(b.AmountUSD)
	if !ok {
		return ops.GrantFacts{}, fmt.Errorf("%w: the request amount is not valid", ErrInvalid)
	}
	return ops.GrantFacts{Open: b.Open, Target: b.Target, AmountMicro: m}, nil
}
