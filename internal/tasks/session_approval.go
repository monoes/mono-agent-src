package tasks

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Who approved a session (task_sessions.approval_basis).
const (
	ApprovalOperator = "operator" // the operator ran `session approve`
	ApprovalLead     = "lead"     // the lead of the parent session, with the operator's agentApproval=lead
	ApprovalPerson   = "person"   // the person answered the person item a giant session raised
)

// Thresholds of giant work (docs/mastermind/plans/2026-10-28-mission-research-directions.md, section 5).
// Giant work is never approved by an agent: it becomes a person item. They are constants on purpose;
// only the USD ceiling is the operator's setting.
const (
	GiantMaxTasks    = 25   // tasks stored in the session, or its max_tasks cap
	GiantBudgetShare = 0.40 // of what the mission's root session has left
)

// ApprovalPolicy is what the operator set for a mission (the root session's coder settings).
type ApprovalPolicy struct {
	AgentLead    bool    // agentApproval=lead
	CeilingUSD   float64 // the most a carve may be; 0 = no absolute ceiling
	WorkingDepth int     // the operator's working max_depth, levels with the root as 1
}

// ApprovalSource reads the operator's settings and keeps the person items of giant sessions. The
// coder host implements it; the store knows nothing of coder sessions beyond this.
type ApprovalSource interface {
	// ApprovalPolicy is the policy of the mission whose root session is linked to rootConversation.
	ApprovalPolicy(ctx context.Context, profileID, rootConversation string) (ApprovalPolicy, error)
	// RaisePersonItem asks the person to approve a session; it waits with no timeout.
	RaisePersonItem(ctx context.Context, profileID, conversation string, session int64) error
	// PersonItemRaised says whether the session has such an item.
	PersonItemRaised(ctx context.Context, profileID string, session int64) (bool, error)
}

// SetApprovalSource wires the settings reader. Without one no agent approves anything.
func (s *Store) SetApprovalSource(src ApprovalSource) { s.approval = src }

// GiantError: the session is giant, so only the person approves it. It matches ErrOperatorOnly.
type GiantError struct {
	Session int64
	Reasons []string
	Raised  bool // a person item was raised
}

func (e *GiantError) Error() string {
	tail := "the person approves it (no item could be raised: tell the operator to run `session approve`)"
	if e.Raised {
		tail = "a person item waits for the person, with no timeout"
	}
	return fmt.Sprintf("session #%d is giant (%s): %s", e.Session, strings.Join(e.Reasons, "; "), tail)
}

func (e *GiantError) Unwrap() error { return ErrOperatorOnly }

func notYourApproval(id int64) error {
	return bindingRefused("approve session #%d: only the operator, or the lead of its parent session when the operator allows it, approves a session", id)
}

// ApproveSession approves a draft's plan: its tasks move to Ready, in their order, and the session
// runs. The operator may always. An agent may only as the lead of the session's parent, bound to
// nothing else, when the operator set agentApproval=lead for the mission, the session has a parent
// and is not giant (see giantReasons); a giant session raises a person item instead. An agent never
// approves a session it leads or sits in, a root session, or one of another mission.
func (s *Store) ApproveSession(ctx context.Context, profileID string, id int64, actor Actor) (Session, error) {
	return s.approveSession(ctx, profileID, id, actor, nil)
}

// approveSession is ApproveSession; a guard (the person's) makes it refuse a plan that moved.
func (s *Store) approveSession(ctx context.Context, profileID string, id int64, actor Actor, guard *PlanGuard) (Session, error) {
	var rootConv string
	var pol ApprovalPolicy
	switch actor.Kind {
	case Human:
	case Agent:
		var err error
		if rootConv, pol, err = s.agentApprovalFacts(ctx, profileID, id, actor); err != nil {
			return Session{}, err
		}
	default:
		return Session{}, operatorOnly("approve a session's plan")
	}
	basis := ApprovalOperator
	if actor.Kind == Human && s.personAnswered(ctx, profileID, id) {
		basis = ApprovalPerson
	}
	var giant *GiantError
	out, err := s.sessionWrite(ctx, profileID, id, func(x dbx, se Session) error {
		if guard != nil {
			if err := s.checkPlanGuard(ctx, x, se, *guard); err != nil {
				return err
			}
		}
		if actor.Kind == Agent {
			g, err := s.checkLeadApproval(ctx, x, profileID, se, actor, rootConv, pol)
			if err != nil {
				return err
			}
			if g != nil {
				giant = g
				return g
			}
			basis = ApprovalLead
		}
		return s.approveDraft(ctx, x, profileID, se, actor, basis)
	})
	if errors.As(err, &giant) {
		giant.Raised = s.approval != nil && s.approval.RaisePersonItem(ctx, profileID, actor.Conversation, id) == nil
		return Session{}, giant
	}
	return out, err
}

// approveDraft moves the draft's tasks to Ready and starts the session, recording who approved.
func (s *Store) approveDraft(ctx context.Context, x dbx, profileID string, se Session, actor Actor, basis string) error {
	id := se.ID
	if se.State != SessionDraft || se.Archived {
		return sessionNotReady("session #%d is %s: only a draft is approved", id, se.State)
	}
	ids, _, err := s.sessionColumn(ctx, x, id, StatusInbox)
	if err != nil {
		return err
	}
	// Inbox cards stack newest on top; the plan's order is the order they were added.
	slices.Sort(ids)
	if se.Counts.Total == 0 {
		return invalid("session #%d has no tasks: plan some before approving", id)
	}
	for _, tid := range ids {
		cur, err := s.getTx(ctx, x, profileID, tid)
		if err != nil {
			return err
		}
		if err := s.moveTx(ctx, x, profileID, cur, StatusReady, Placement{Bottom: true}, actor, "moved", "approved with the plan"); err != nil {
			return err
		}
	}
	if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET state = 'running', approved_by = ?, approved_at = ?, approval_basis = ? WHERE id = ?`,
		actor.Label(), s.stamp(), basis, id); err != nil {
		return fmt.Errorf("tasks: approving session #%d: %w", id, err)
	}
	return s.sessionEvent(ctx, x, id, actor, "approved", fmt.Sprintf("%d tasks to Ready (basis %s)", len(ids), basis))
}

// personAnswered says whether the person item of a giant session is the reason the operator approves.
func (s *Store) personAnswered(ctx context.Context, profileID string, id int64) bool {
	if s.approval == nil {
		return false
	}
	raised, err := s.approval.PersonItemRaised(ctx, profileID, id)
	return err == nil && raised
}

// agentApprovalFacts is read before the write transaction (the settings live in another store): it
// refuses an agent that is not the lead of the parent, and returns the root's chat and the policy.
func (s *Store) agentApprovalFacts(ctx context.Context, profileID string, id int64, actor Actor) (string, ApprovalPolicy, error) {
	var rootConv string
	err := s.snapshot(ctx, func(x dbx) error {
		se, err := s.sessionTx(ctx, x, profileID, id)
		if err != nil {
			return notYourApproval(id) // not found reads the same as not yours
		}
		parent, root, err := s.approvalChain(ctx, x, profileID, se)
		if err != nil {
			return err
		}
		if err := leadOfParent(se, actor, parent); err != nil {
			return err
		}
		rootConv = root.ConversationID
		return nil
	})
	if err != nil {
		return "", ApprovalPolicy{}, err
	}
	if s.approval == nil || rootConv == "" {
		return "", ApprovalPolicy{}, operatorOnly("approve a session: the operator has not allowed leads to approve (agentApproval)")
	}
	pol, err := s.approval.ApprovalPolicy(ctx, profileID, rootConv)
	if err != nil {
		return "", ApprovalPolicy{}, operatorOnly("approve a session: the operator's approval setting could not be read")
	}
	return rootConv, pol, nil
}

// approvalChain reads the parent and the root of the session's chain (the mission's root session).
func (s *Store) approvalChain(ctx context.Context, x dbx, profileID string, se Session) (parent, root Session, err error) {
	if se.ParentSessionID == 0 {
		return Session{}, Session{}, operatorOnly(fmt.Sprintf("approve session #%d: it is a mission's root, which only the operator approves", se.ID))
	}
	chain, err := s.sessionChain(ctx, x, se.ID)
	if err != nil {
		return Session{}, Session{}, err
	}
	if parent, err = s.sessionTx(ctx, x, profileID, se.ParentSessionID); err != nil {
		return Session{}, Session{}, notYourApproval(se.ID)
	}
	if root, err = s.sessionTx(ctx, x, profileID, chain[len(chain)-1]); err != nil {
		return Session{}, Session{}, notYourApproval(se.ID)
	}
	return parent, root, nil
}

// leadOfParent is nil when the actor is the verified lead of the session's parent and nothing
// else: bound to no task, to no session but the parent, and its verified chat (set only by trusted
// code from a verified token) is the chat the parent is linked to and not the session's own.
func leadOfParent(se Session, actor Actor, parent Session) error {
	if actor.Task != 0 || actor.Verifier {
		return bindingRefused("this agent is bound to task #%d: it cannot approve a session", actor.Task)
	}
	if err := needName(actor); err != nil {
		return err
	}
	switch {
	case parent.ID == 0 || parent.ConversationID == "" || actor.Conversation != parent.ConversationID:
	case actor.Session != 0 && actor.Session != parent.ID:
	case se.ConversationID != "" && se.ConversationID == actor.Conversation:
	default:
		return nil
	}
	return notYourApproval(se.ID)
}

// checkLeadApproval is the agent's rule, run inside the write transaction. It returns a GiantError
// (and no error) when the session is giant.
func (s *Store) checkLeadApproval(ctx context.Context, x dbx, profileID string, se Session, actor Actor, seenRoot string, pol ApprovalPolicy) (*GiantError, error) {
	parent, root, err := s.approvalChain(ctx, x, profileID, se)
	if err != nil {
		return nil, err
	}
	if err := leadOfParent(se, actor, parent); err != nil {
		return nil, err
	}
	if root.ConversationID != seenRoot {
		return nil, operatorOnly("approve a session: its mission changed while it was checked; try again")
	}
	if !pol.AgentLead {
		return nil, operatorOnly(fmt.Sprintf("approve session #%d: the operator has not allowed leads to approve (agentApproval is off)", se.ID))
	}
	if root, err = s.withSiblingsCommitted(ctx, x, se, root); err != nil {
		return nil, err
	}
	if reasons := giantReasons(se, parent, root, pol); len(reasons) > 0 {
		return &GiantError{Session: se.ID, Reasons: reasons}, nil
	}
	return nil, nil
}

// accessRank orders the access levels; one the store does not know ranks above them all.
func accessRank(level string) int {
	if i := slices.Index(AccessLevels, level); i >= 0 {
		return i
	}
	return len(AccessLevels)
}

// giantReasons says why a session is giant work, which only the person approves; empty when it
// is not. The budget rules count the budget the session holds (an inherited one included).
func giantReasons(se, parent, root Session, pol ApprovalPolicy) []string {
	var r []string
	if n := se.Counts.Stored(); n > GiantMaxTasks {
		r = append(r, fmt.Sprintf("%d tasks, over %d", n, GiantMaxTasks))
	}
	if se.MaxTasks > GiantMaxTasks {
		r = append(r, fmt.Sprintf("max tasks %d, over %d", se.MaxTasks, GiantMaxTasks))
	}
	if pol.CeilingUSD > 0 && (se.BudgetUSD == 0 || se.BudgetUSD > pol.CeilingUSD) {
		r = append(r, fmt.Sprintf("budget %s is not within the operator's ceiling of %s", money(se.BudgetUSD), money(pol.CeilingUSD)))
	}
	if se.BudgetUSD == 0 && root.BudgetUSD == 0 && pol.CeilingUSD == 0 {
		r = append(r, "no budget set: the person decides")
	}
	if root.BudgetUSD > 0 {
		left := root.BudgetUSD - root.SpentUSD
		if left <= 0 || se.BudgetUSD == 0 || se.BudgetUSD > GiantBudgetShare*left {
			r = append(r, fmt.Sprintf("budget %s is over %d%% of the mission's %s left", money(se.BudgetUSD), int(GiantBudgetShare*100), money(max(left, 0))))
		}
	}
	if accessRank(se.AccessLevel) > accessRank(parent.AccessLevel) {
		r = append(r, fmt.Sprintf("access %q is above the parent's %q", se.AccessLevel, parent.AccessLevel))
	}
	if work := min(max(pol.WorkingDepth, 1), HardMaxSessionDepth); se.Depth > work {
		r = append(r, fmt.Sprintf("level %d is below the working depth %d", se.Depth, work))
	}
	return r
}

func money(f float64) string { return fmt.Sprintf("$%.2f", f) }
