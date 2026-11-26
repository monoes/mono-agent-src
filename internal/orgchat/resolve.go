package orgchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// Client is the monomind surface resolving goes through; tests fake it.
type Client interface {
	Status(ctx context.Context, root, org string) (json.RawMessage, error)
	// All lists every item of kind ("questions", "approvals", "gates"),
	// resolved ones included.
	All(ctx context.Context, root, org, kind string) (json.RawMessage, error)
	Answer(ctx context.Context, root, org, questionID, answer string) error
	// Dismiss closes a question without an answer; reason may be empty.
	Dismiss(ctx context.Context, root, org, questionID, reason string) error
	Approve(ctx context.Context, root, org, role, action string, approve bool, requestID string) error
	Gate(ctx context.Context, root, org, gateID string, approve bool, resolution string) error
}

// Resolver is who the chat resolves items as.
const Resolver = "human"

// MonomindClient is Client over the monomind proxies.
type MonomindClient struct{}

func (MonomindClient) Status(ctx context.Context, root, org string) (json.RawMessage, error) {
	return monomind.OrgStatus(ctx, root, org)
}
func (MonomindClient) All(ctx context.Context, root, org, kind string) (json.RawMessage, error) {
	return monomind.OrgHumanItemsAll(ctx, root, org, kind)
}
func (MonomindClient) Answer(ctx context.Context, root, org, questionID, answer string) error {
	_, err := monomind.OrgAnswerWith(ctx, root, org, questionID, answer, monomind.ResolveOptions{By: Resolver})
	return err
}
func (MonomindClient) Dismiss(ctx context.Context, root, org, questionID, reason string) error {
	_, err := monomind.OrgDismissQuestionWith(ctx, root, org, questionID, reason, monomind.ResolveOptions{By: Resolver})
	return err
}
func (MonomindClient) Approve(ctx context.Context, root, org, role, action string, approve bool, requestID string) error {
	opts := monomind.ResolveOptions{By: Resolver, RequestID: requestID}
	var err error
	if approve {
		_, err = monomind.OrgApproveWith(ctx, root, org, role, action, opts)
	} else {
		_, err = monomind.OrgDenyWith(ctx, root, org, role, action, opts)
	}
	return err
}
func (MonomindClient) Gate(ctx context.Context, root, org, gateID string, approve bool, resolution string) error {
	_, err := monomind.OrgGateResolveWith(ctx, root, org, gateID, approve, resolution, monomind.ResolveOptions{By: Resolver})
	return err
}

// ErrNotFound: the org has no item by that ref.
var ErrNotFound = errors.New("orgchat: no such item")

// NotRunningError refuses to resolve a pending item of an org that is not
// running: nothing was sent, and the item stays pending for when it runs.
type NotRunningError struct {
	Org    string
	Status string
}

func (e *NotRunningError) Error() string {
	st := e.Status
	if st == "" {
		st = "unknown"
	}
	return fmt.Sprintf("org %q is not running (status: %s); nothing was sent — start or resume it, then try again", e.Org, st)
}

// UnnamedRefError refuses approving an approval by a ref other than its
// request id (a bare role:action): it names whichever call is pending now,
// which need not be the one the approver saw.
type UnnamedRefError struct{ Org, Ref, RequestID string }

func (e *UnnamedRefError) Error() string {
	return fmt.Sprintf("%q does not name one request, so nothing was sent; look at it with `org approvals %s`, then approve it by its request id %q",
		e.Ref, e.Org, e.RequestID)
}

// Request resolves one item: answer a question (Text), dismiss one
// (Dismiss; Text is the optional reason), or approve or deny (Approve) an
// approval or a gate. Text on a gate is its resolution note. An approval is
// approved only by its request id (Ref).
type Request struct {
	Org     string
	Ref     string
	Answer  bool // a question; otherwise an approval or a gate
	Dismiss bool // a question, closed without an answer
	Approve bool
	Text    string
	// Allow, when set, is asked before an approval is approved, with its
	// action; its error refuses it and nothing is sent.
	Allow func(action string) error
}

// Result is what resolving did. Already means the item had been resolved
// before (by this chat, another window, the decider or the CLI) and nothing
// was sent; State is how it ended, which may differ from what was asked.
type Result struct {
	OK      bool   `json:"ok"`
	Org     string `json:"org"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	State   string `json:"state"`
	Already bool   `json:"already"`
}

// running reads `org status` for a live run; a paused org still takes
// answers.
func running(ctx context.Context, c Client, root, org string) (bool, string, error) {
	raw, err := c.Status(ctx, root, org)
	if err != nil {
		return false, "", err
	}
	var st struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return false, "", fmt.Errorf("orgchat: status: %w", err)
	}
	return st.Status == "running", st.Status, nil
}

// Resolve resolves one item, at most once. Under a per-org lock it reads
// the item: one already resolved returns its state with Already and sends
// nothing, so a retried or doubled click is harmless; a pending one is
// resolved only while the org runs (NotRunningError otherwise). Dismissing a
// question is the exception: monomind records it and queues a note for the
// asking role when the org is stopped, so a question left over from the
// last run can be cleared without starting the org.
func Resolve(ctx context.Context, c Client, root string, req Request) (*Result, error) {
	if !orgdesign.ValidOrgName(req.Org) {
		return nil, fmt.Errorf("orgchat: invalid org name %q", req.Org)
	}
	if req.Answer && !req.Dismiss && strings.TrimSpace(req.Text) == "" {
		return nil, errors.New("orgchat: an answer needs text")
	}
	unlock, err := lockOrg(ctx, root, req.Org)
	if err != nil {
		return nil, err
	}
	defer unlock()

	res := &Result{OK: true, Org: req.Org, Ref: req.Ref}
	var act func() error
	switch {
	case req.Answer || req.Dismiss:
		res.Kind = KindQuestion
		raw, err := c.All(ctx, root, req.Org, "questions")
		if err != nil {
			return nil, err
		}
		list, err := ParseQuestions(raw)
		if err != nil {
			return nil, err
		}
		var q *Question
		for i := range list {
			if list[i].QuestionID == req.Ref {
				q = &list[i]
			}
		}
		if q == nil {
			return nil, ErrNotFound
		}
		if q.State == StateDismissed {
			res.State, res.Already = StateDismissed, true
			return res, nil
		}
		if q.Answer != nil {
			res.State, res.Already = StateAnswered, true
			return res, nil
		}
		if req.Dismiss {
			res.State = StateDismissed
			act = func() error { return c.Dismiss(ctx, root, req.Org, q.QuestionID, strings.TrimSpace(req.Text)) }
			break
		}
		res.State = StateAnswered
		act = func() error { return c.Answer(ctx, root, req.Org, q.QuestionID, req.Text) }
	case strings.HasPrefix(req.Ref, "gate-"):
		res.Kind = KindGate
		raw, err := c.All(ctx, root, req.Org, "gates")
		if err != nil {
			return nil, err
		}
		list, err := ParseGates(raw)
		if err != nil {
			return nil, err
		}
		var g *Gate
		for i := range list {
			if list[i].ID == req.Ref {
				g = &list[i]
			}
		}
		if g == nil {
			return nil, ErrNotFound
		}
		if st := gateState(*g); st != "" {
			res.State, res.Already = st, true
			return res, nil
		}
		res.State = StateRejected
		if req.Approve {
			res.State = StateApproved
		}
		act = func() error { return c.Gate(ctx, root, req.Org, g.ID, req.Approve, req.Text) }
	default:
		res.Kind = KindApproval
		raw, err := c.All(ctx, root, req.Org, "approvals")
		if err != nil {
			return nil, err
		}
		list, err := ParseApprovals(raw)
		if err != nil {
			return nil, err
		}
		a, ok := findApproval(list, req.Ref)
		if !ok {
			return nil, ErrNotFound
		}
		res.Ref = approvalRef(a)
		if st := approvalState(a); st != "" {
			res.State, res.Already = st, true
			return res, nil
		}
		requestID := ""
		if a.RequestID != nil {
			requestID = *a.RequestID
		}
		res.State = StateDenied
		if req.Approve {
			res.State = StateApproved
			if req.Allow != nil {
				if err := req.Allow(a.Action); err != nil {
					return nil, err
				}
			}
			// Only the one request the approver named is ever released.
			if requestID == "" {
				return nil, &monomind.NoRequestIDError{Org: req.Org, Role: a.RoleID, Action: a.Action}
			}
			if req.Ref != requestID {
				return nil, &UnnamedRefError{Org: req.Org, Ref: req.Ref, RequestID: requestID}
			}
		}
		act = func() error { return c.Approve(ctx, root, req.Org, a.RoleID, a.Action, req.Approve, requestID) }
	}

	if !req.Dismiss {
		live, status, err := running(ctx, c, root, req.Org)
		if err != nil {
			return nil, err
		}
		if !live {
			return nil, &NotRunningError{Org: req.Org, Status: status}
		}
	}
	if err := act(); err != nil {
		return nil, err
	}
	return res, nil
}

// Resolving is a few monomind calls: a lock held longer than this belongs
// to something stuck, and the caller is told so instead of waiting on.
const (
	lockPollInterval = 50 * time.Millisecond
	lockWaitTimeout  = 90 * time.Second
)

// lockOrg serializes resolving within one org folder across processes, so
// two clicks racing can't both see an item pending and both send. The lock
// lives in the folder's own .monomind directory (the person's, never a
// shared temp dir). It is an OS file lock (flock, or LockFileEx on
// Windows) the kernel drops if its holder dies, so nothing is ever stale;
// the wait is bounded and ends with ctx.
func lockOrg(ctx context.Context, root, org string) (func(), error) {
	path := filepath.Join(root, ".monomind", "locks", "orgchat-"+org+".lock")
	deadline := time.Now().Add(lockWaitTimeout)
	for {
		release, err := daemonhb.LockFile(path)
		if err == nil {
			return release, nil
		}
		if !errors.Is(err, daemonhb.ErrHeld) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another process has held %s for %s", filepath.Base(path), lockWaitTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPollInterval):
		}
	}
}
