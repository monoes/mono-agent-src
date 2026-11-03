package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/monoes/mono-agent/internal/tasks"
)

// CanonVersion versions the canonical argument encoding; it is inside every hash, so a rule change can never make
// an old hash match a new form.
const CanonVersion = 1

// ArgsHash is the SHA-256 of the canonical JSON of the resolved arguments, domain-separated by the operation id
// and CanonVersion. args must be a struct (field order is the encoding order).
func ArgsHash(op string, args any) (string, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "monoagent/doomsday/args\x00%s\x00%d\x00", op, CanonVersion)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Active is the live charter as the store reports it.
type Active struct {
	ID          string
	Hash        string
	Generation  int64
	Profile     string
	Constraints Constraints
}

// Charters finds the live (running) charter of a profile. ok is false when there is none.
type Charters interface {
	Active(ctx context.Context, profile string) (a Active, ok bool, err error)
}

// Subject is what the store says a plan or ask is, read at the effect and never taken from the approval or the
// agent. Open is whether it still awaits an answer.
type Subject struct {
	Open       bool
	Rev        int64
	Digest     string
	Class      string // the ask's class (ask.answer only)
	Tasks      int
	SpendMicro int64 // the spend it would commit, cumulative committed spend of its tree included
	Access     string
	Depth      int
	// NeverLiftable is true when approving would grant a credential or change a control on the never-liftable list
	// (spec 4.2): such a subject is never auto-approved, whatever its numbers.
	NeverLiftable bool
}

// GrantFacts is the open budget request a grant answers.
type GrantFacts struct {
	Open        bool
	Target      string
	AmountMicro int64
}

// Facts reads the subject of a request from the store.
type Facts interface {
	Plan(ctx context.Context, profile string, session int64) (Subject, error)
	Ask(ctx context.Context, profile string, askID int64) (Subject, error)
	Grant(ctx context.Context, profile string, requestID int64) (GrantFacts, error)
}

// Spend is one cumulative counter to charge: N units of Kind, allowed while the sum stays at or under Cap.
type Spend struct {
	Kind string
	N    int64
	Cap  int64
}

// AuditRow is an audit entry of the decision. The ledger writes it in the consume's own transaction; the auditor
// writes the rows of parks.
type AuditRow struct {
	Profile     string
	CharterID   string
	Generation  int64
	Op          string
	ApprovalID  string
	Result      string // approved | parked
	Exceeded    []string
	Limits      Constraints // the limits it was decided under
	Actual      Subject
	ActualGrant GrantFacts
	ArgsHash    string
}

// Consume charges all Spends and records the approval as spent, atomically: either every Spend fits under its cap
// and the approval id is new, and the consumed row, the counter deltas and the audit row are all written in one
// transaction, or nothing is. Over lists the kinds that did not fit. Replay is true when ApprovalID was already
// consumed (nothing is charged again).
type Consume struct {
	Profile    string
	CharterID  string
	Generation int64
	ApprovalID string
	Spends     []Spend
	Audit      AuditRow
}

// ConsumeResult is the outcome of Ledger.Consume.
type ConsumeResult struct {
	Done   bool
	Replay bool
	Over   []string
}

// Ledger is the store's cumulative counters (note 13.4: a conditional insert of SUM(n)+n <= cap in the same
// statement and transaction as the consumed row).
type Ledger interface {
	Consume(ctx context.Context, c Consume) (ConsumeResult, error)
}

// Auditor appends a row to the audit stream.
type Auditor interface {
	Append(ctx context.Context, r AuditRow) error
}

// Park is a person item for what is over a limit. The work that depends on it waits; nothing resolves it but a
// person.
type Park struct {
	Profile  string
	Op       string
	Subject  string
	Exceeded []string
}

// Parker raises the person item and blocks dependants.
type Parker interface {
	Park(ctx context.Context, p Park) error
}

// Caller is the session whose verified token result is the presenter. The host sets it from the token, never from
// an argument.
type Caller struct{ Session int64 }

// Request asks for one typed operation. Exactly the field of Op is set.
type Request struct {
	Profile    string
	CharterID  string
	Hash       string
	Generation int64
	Op         string
	ApprovalID string
	Caller     Caller
	Addressed  int64 // the session the approval is addressed to
	Plan       *PlanArgs
	Answer     *AnswerArgs
	Grant      *GrantArgs
}

// PlanArgs are the arguments of plan.approve the boss saw: the session, its revision and digest. The counts are
// re-derived from the store.
type PlanArgs struct {
	Session int64  `json:"session"`
	Rev     int64  `json:"rev"`
	Digest  string `json:"digest"`
}

// AnswerArgs are the arguments of ask.answer.
type AnswerArgs struct {
	AskID int64  `json:"ask"`
	Class string `json:"class"`
}

// GrantArgs are the arguments of spend.grant: the open budget request, its target and the integer amount.
type GrantArgs struct {
	RequestID   int64  `json:"request"`
	Target      string `json:"target"`
	AmountMicro int64  `json:"amount_micro"`
}

// Verdict is what Decide decided.
type Verdict int

const (
	// Approved: the approval is spent, the counters charged and the audit row written; the caller performs the
	// effect (and only then).
	Approved Verdict = iota + 1
	// Parked: over a limit or not lifted; a person item was raised and nothing was spent.
	Parked
)

// Decision is the result of Decide.
type Decision struct {
	Verdict  Verdict
	Exceeded []string
	ArgsHash string
}

// Enforcer is the one enforcement function of this package.
type Enforcer struct {
	Charters Charters
	Facts    Facts
	Ledger   Ledger
	Auditor  Auditor
	Parker   Parker
	Bounds   SpendBounds
}

// Decide checks a request for a typed operation against the live charter and the actual plan, ask or grant, and
// either spends the approval (Approved), parks it as a person item (Parked) or refuses it (an error). With no
// charter it refuses before it reads anything else.
func (e *Enforcer) Decide(ctx context.Context, r Request) (Decision, error) {
	if !Allowed(r.Op) {
		return Decision{}, ErrNotAllowlisted
	}
	act, ok, err := e.Charters.Active(ctx, r.Profile)
	if err != nil {
		return Decision{}, err
	}
	if !ok {
		return Decision{}, ErrNoCharter
	}
	if act.Hash != r.Hash || act.ID != r.CharterID || act.Generation != r.Generation || act.Profile != r.Profile || act.Hash == "" {
		return Decision{}, ErrCharterStale
	}
	if err := act.Constraints.Validate(e.Bounds); err != nil {
		return Decision{}, fmt.Errorf("%w (%v)", ErrNotEnabled, err)
	}
	if r.Caller.Session == 0 || r.Caller.Session != r.Addressed {
		return Decision{}, ErrCallerMismatch
	}
	if r.ApprovalID == "" {
		return Decision{}, fmt.Errorf("%w: no approval id", ErrBadArguments)
	}
	switch r.Op {
	case OpPlanApprove:
		return e.decidePlan(ctx, r, act)
	case OpAskAnswer:
		return e.decideAnswer(ctx, r, act)
	default:
		return e.decideGrant(ctx, r, act)
	}
}

func (e *Enforcer) decidePlan(ctx context.Context, r Request, act Active) (Decision, error) {
	c := act.Constraints.PlanApprove
	if c == nil {
		return Decision{}, ErrNotEnabled
	}
	if r.Plan == nil {
		return Decision{}, fmt.Errorf("%w: plan.approve needs a plan", ErrBadArguments)
	}
	hash, err := ArgsHash(r.Op, r.Plan)
	if err != nil {
		return Decision{}, err
	}
	sub, err := e.Facts.Plan(ctx, r.Profile, r.Plan.Session)
	if err != nil {
		return Decision{}, err
	}
	if !sub.Open {
		return Decision{}, ErrNotOpen
	}
	if sub.Rev != r.Plan.Rev || sub.Digest != r.Plan.Digest {
		return Decision{}, ErrPlanChanged
	}
	return e.finishAsk(ctx, r, act, c.AskLimits, sub, hash, fmt.Sprintf("plan of session #%d", r.Plan.Session))
}

func (e *Enforcer) decideAnswer(ctx context.Context, r Request, act Active) (Decision, error) {
	c := act.Constraints.AskAnswer
	if c == nil {
		return Decision{}, ErrNotEnabled
	}
	if r.Answer == nil {
		return Decision{}, fmt.Errorf("%w: ask.answer needs an ask", ErrBadArguments)
	}
	hash, err := ArgsHash(r.Op, r.Answer)
	if err != nil {
		return Decision{}, err
	}
	sub, err := e.Facts.Ask(ctx, r.Profile, r.Answer.AskID)
	if err != nil {
		return Decision{}, err
	}
	if !sub.Open {
		return Decision{}, ErrNotOpen
	}
	if sub.Class != r.Answer.Class {
		return Decision{}, fmt.Errorf("%w: the ask's class is not the class named", ErrBadArguments)
	}
	var over []string
	if !slices.Contains(c.Classes, sub.Class) {
		over = append(over, LimitClass) // a class the charter did not lift: nobody answers, it stays parked
	}
	return e.finish(ctx, r, act, c.AskLimits, sub, hash, fmt.Sprintf("ask #%d", r.Answer.AskID), over)
}

func (e *Enforcer) finishAsk(ctx context.Context, r Request, act Active, l AskLimits, sub Subject, hash, label string) (Decision, error) {
	return e.finish(ctx, r, act, l, sub, hash, label, nil)
}

// finish applies every per-ask limit to the actual values, then charges the cumulative totals atomically.
func (e *Enforcer) finish(ctx context.Context, r Request, act Active, l AskLimits, sub Subject, hash, label string, over []string) (Decision, error) {
	if sub.SpendMicro < 0 || sub.Tasks < 0 || sub.Depth < 0 {
		return Decision{}, fmt.Errorf("%w: negative facts", ErrBadArguments)
	}
	rank := slices.Index(tasks.AccessLevels, sub.Access)
	switch {
	case sub.NeverLiftable || sub.Access == "full" || rank < 0:
		over = append(over, LimitNeverLiftable)
	case rank > slices.Index(tasks.AccessLevels, l.Access):
		over = append(over, LimitAskAccess)
	}
	if sub.SpendMicro > l.SpendMicro {
		over = append(over, LimitAskSpend)
	}
	if sub.Tasks > l.Tasks {
		over = append(over, LimitAskTasks)
	}
	if sub.Depth > l.Depth {
		over = append(over, LimitAskDepth)
	}
	audit := AuditRow{Profile: r.Profile, CharterID: act.ID, Generation: act.Generation, Op: r.Op, ApprovalID: r.ApprovalID,
		Limits: act.Constraints, Actual: sub, ArgsHash: hash}
	if len(over) > 0 {
		return e.park(ctx, r, audit, label, over)
	}
	res, err := e.Ledger.Consume(ctx, Consume{Profile: r.Profile, CharterID: act.ID, Generation: act.Generation, ApprovalID: r.ApprovalID,
		Spends: []Spend{
			{Kind: KindAskSpend, N: sub.SpendMicro, Cap: act.Constraints.Run.SpendMicro},
			{Kind: KindAskTasks, N: int64(sub.Tasks), Cap: act.Constraints.Run.Tasks},
		}, Audit: withResult(audit, "approved", nil)})
	return e.settle(ctx, r, audit, label, hash, res, err, map[string]string{KindAskSpend: LimitRunSpend, KindAskTasks: LimitRunTasks})
}

func (e *Enforcer) decideGrant(ctx context.Context, r Request, act Active) (Decision, error) {
	c := act.Constraints.SpendGrant
	if c == nil {
		return Decision{}, ErrNotEnabled
	}
	g := r.Grant
	if g == nil {
		return Decision{}, fmt.Errorf("%w: spend.grant needs a request", ErrBadArguments)
	}
	if g.AmountMicro < e.Bounds.MinMicro || g.AmountMicro > e.Bounds.MaxMicro {
		return Decision{}, fmt.Errorf("%w: a grant is %d to %d micro-dollars", ErrBadArguments, e.Bounds.MinMicro, e.Bounds.MaxMicro)
	}
	hash, err := ArgsHash(r.Op, g)
	if err != nil {
		return Decision{}, err
	}
	facts, err := e.Facts.Grant(ctx, r.Profile, g.RequestID)
	if err != nil {
		return Decision{}, err
	}
	if !facts.Open {
		return Decision{}, ErrNotOpen
	}
	if facts.Target != g.Target || facts.AmountMicro < g.AmountMicro {
		return Decision{}, fmt.Errorf("%w: the grant does not match the open request", ErrBadArguments)
	}
	audit := AuditRow{Profile: r.Profile, CharterID: act.ID, Generation: act.Generation, Op: r.Op, ApprovalID: r.ApprovalID,
		Limits: act.Constraints, ActualGrant: facts, ArgsHash: hash}
	var over []string
	if !slices.Contains(c.Targets, g.Target) {
		over = append(over, LimitGrantTarget)
	}
	if g.AmountMicro > c.MaxPerGrantMicro {
		over = append(over, LimitGrantMax)
	}
	label := fmt.Sprintf("budget request #%d", g.RequestID)
	if len(over) > 0 {
		return e.park(ctx, r, audit, label, over)
	}
	res, err := e.Ledger.Consume(ctx, Consume{Profile: r.Profile, CharterID: act.ID, Generation: act.Generation, ApprovalID: r.ApprovalID,
		Spends: []Spend{{Kind: KindGrantSpend, N: g.AmountMicro, Cap: c.RunCeilingMicro}},
		Audit:  withResult(audit, "approved", nil)})
	return e.settle(ctx, r, audit, label, hash, res, err, map[string]string{KindGrantSpend: LimitGrantRun})
}

func withResult(a AuditRow, result string, over []string) AuditRow {
	a.Result, a.Exceeded = result, over
	return a
}

// settle turns the ledger's answer into a decision: a replay is refused (no double count), an over-cap parks.
func (e *Enforcer) settle(ctx context.Context, r Request, audit AuditRow, label, hash string, res ConsumeResult, err error, names map[string]string) (Decision, error) {
	if err != nil {
		return Decision{}, err
	}
	if res.Replay {
		return Decision{}, ErrAlreadyConsumed
	}
	if !res.Done {
		var over []string
		for _, k := range res.Over {
			if n, ok := names[k]; ok {
				over = append(over, n)
			}
		}
		if len(over) == 0 {
			over = []string{"cumulative"} // the ledger refused for a reason this table does not know: still not approved
		}
		return e.park(ctx, r, audit, label, over)
	}
	return Decision{Verdict: Approved, ArgsHash: hash}, nil
}

// park records the over-limit decision, raises the person item and returns Parked. If either step fails the error
// is returned and nothing is approved (fail closed).
func (e *Enforcer) park(ctx context.Context, r Request, audit AuditRow, label string, over []string) (Decision, error) {
	if err := e.Auditor.Append(ctx, withResult(audit, "parked", over)); err != nil {
		return Decision{}, err
	}
	if err := e.Parker.Park(ctx, Park{Profile: r.Profile, Op: r.Op, Subject: label, Exceeded: over}); err != nil {
		return Decision{}, err
	}
	return Decision{Verdict: Parked, Exceeded: over, ArgsHash: audit.ArgsHash}, nil
}
