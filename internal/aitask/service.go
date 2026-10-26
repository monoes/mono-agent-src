package aitask

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

// Env names the variables a task's turn runs with: the task and the token that
// lets its holder, and no one else, complete or fail it.
const (
	EnvTaskID = "MONOAGENT_AITASK_ID"
	EnvToken  = "MONOAGENT_AITASK_TOKEN"
)

// Caps on what a task carries.
const (
	MaxGoalBytes   = 16 << 10
	MaxResultBytes = 4 << 20
	maxErrorBytes  = 2000
)

// Conversation is the coder conversation a task runs in.
type Conversation struct {
	ID      string
	Cwd     string
	Runtime string
}

// Launcher is what a task needs from the rest of the CLI: a coder
// conversation and a detached process that runs its first turn. The CLI
// implements it over `chat history create --mode coder` and `chat --turn`
// (the code coder queue start uses); tests stand in for it.
type Launcher interface {
	// Ready is nil when a coder conversation can be created, else the refusal
	// (an *Error with CodeCoderDisabled when coder mode is off).
	Ready(ctx context.Context) error
	// Policy is a fingerprint of the settings a task runs under (the coder
	// settings): `start` refuses a task whose policy has changed since submit.
	Policy(ctx context.Context) (string, error)
	// Confined says whether this profile's tasks run inside a verified
	// confinement (not a guess: sandboxprobe is advisory). Unattended tasks
	// need it.
	Confined(ctx context.Context) bool
	// DefaultBudget is the spend cap, in USD, a task gets when submit names
	// none (the operator's coder budget).
	DefaultBudget(ctx context.Context) (float64, error)
	// Enforceable is nil when a turn of this kind can be held to this access
	// level, else the refusal (CodeAccessUnenforced). Submit's
	// CreateConversation applies it, and Start checks it again, so a kind
	// that stops being confined while a task is queued does not start as an
	// unconfined shell turn.
	Enforceable(kind, access string) error
	// CreateConversation makes the task's conversation at the level the task's
	// access stands for, or refuses with CodeAccessUnenforced when the kind
	// cannot be held to that level.
	CreateConversation(ctx context.Context, runtime, kind, access string) (Conversation, error)
	// Detach starts the process that runs task id's turn, with EnvTaskID and
	// EnvToken in its environment, and returns without waiting for it.
	Detach(ctx context.Context, taskID, token string) error
}

// Service is the task operations over a store and a launcher.
type Service struct {
	Store    *Store
	Launcher Launcher
	// Redact hides secret values in the context and the text an agent hands
	// back; nil leaves text as it is.
	Redact    func(string) string
	PollEvery time.Duration
	// StateDir is the folder next to the database; verified context
	// snapshots live under it (SnapshotContext).
	StateDir string
	// Admit, when set, reserves the task's spend cap at start and returns the
	// cap its turn runs under; its refusal keeps the task queued (budget).
	Admit func(context.Context, Task) (float64, error)
	// Remote says the caller is the daemon API acting for a task's session
	// rather than that session's own process: Complete and Fail check the task
	// token's hash but not the process tree (Store.CheckTokenRemote).
	Remote bool
}

// holds says whether token is task id's token for this service's caller.
func (s *Service) holds(id, token string) bool {
	if s.Remote {
		return s.Store.CheckTokenRemote(id, token)
	}
	return s.Store.CheckToken(id, token)
}

// SubmitInput is `aitask submit`.
type SubmitInput struct {
	Kind          string
	Goal          string
	Context       []ContextSource
	ApplicationID string
	Access        string
	Runtime       string
	Unattended    bool
	QueueOnly     bool
	// BudgetUSD is the spend cap; nil takes the launcher's default. 0 is no
	// spend and the task is refused: it is never read as unlimited.
	BudgetUSD *float64
}

// Submit creates the task and its coder conversation, writes its context and
// starts turn 1 (unless QueueOnly). It never falls back to anything: when
// coder mode is off it says so with CodeCoderDisabled, and the caller decides.
func (s *Service) Submit(ctx context.Context, in SubmitInput) (Task, error) {
	goal := strings.TrimSpace(in.Goal)
	switch {
	case goal == "":
		return Task{}, errf(CodeInvalidInput, "--goal is required")
	case len(goal) > MaxGoalBytes || !utf8.ValidString(goal):
		return Task{}, errf(CodeInvalidInput, "--goal must be UTF-8 text of at most %d KB", MaxGoalBytes>>10)
	case strings.ContainsAny(in.ApplicationID, "\x00\r\n") || len(in.ApplicationID) > 200:
		return Task{}, errf(CodeInvalidInput, "invalid --application")
	case !ValidAccess(in.Access):
		return Task{}, errf(CodeInvalidInput, "--access must be coding, workspace or qa")
	}
	kind, err := Lookup(in.Kind)
	if err != nil {
		return Task{}, err
	}
	access := in.Access
	if access == "" {
		access = kind.DefaultAccess
	}
	if access == "" {
		access = AccessCoding
	}
	if AccessRank(access) > AccessRank(kind.Ceiling()) {
		return Task{}, errf(CodeAccessRaised, "kind %s allows at most --access %s, not %s", kind.Name, kind.Ceiling(), access)
	}
	if err := s.checkUnattended(ctx, kind, access, in.Unattended); err != nil {
		return Task{}, err
	}
	if err := s.Launcher.Ready(ctx); err != nil {
		return Task{}, err
	}
	budget, err := s.budget(ctx, in.BudgetUSD)
	if err != nil {
		return Task{}, err
	}
	conv, err := s.Launcher.CreateConversation(ctx, in.Runtime, kind.Name, access)
	if err != nil {
		return Task{}, err
	}
	t := Task{ID: NewID(), Kind: kind.Name, ConversationID: conv.ID, Status: StatusQueued, Goal: goal,
		ApplicationID: in.ApplicationID, Access: access, Runtime: conv.Runtime, Unattended: in.Unattended,
		BudgetUSD: budget, Profile: s.Store.Profile}
	files, err := WriteContext(conv.Cwd, t.ID, in.Context, s.Redact)
	if err != nil {
		return Task{}, err
	}
	if t.Prompt, err = BuildPrompt(kind, t, files); err != nil {
		return Task{}, err
	}
	t.Cwd = conv.Cwd
	if t.ContextHash, err = ContextHash(conv.Cwd, t.ID); err != nil {
		return Task{}, err
	}
	policy, err := s.Launcher.Policy(ctx)
	if err != nil {
		return Task{}, err
	}
	t.CapsHash = CapsHash(t, kind, policy)
	if t, err = s.Store.Insert(t); err != nil {
		return Task{}, err
	}
	if in.QueueOnly {
		return t, nil
	}
	return s.Start(ctx, t.ID)
}

// budget is the cap a task gets: the one asked for, else the default. Zero
// or less is "no spend", which no task can run on.
func (s *Service) budget(ctx context.Context, asked *float64) (float64, error) {
	b := 0.0
	if asked != nil {
		b = *asked
	} else {
		var err error
		if b, err = s.Launcher.DefaultBudget(ctx); err != nil {
			return 0, err
		}
	}
	if b <= 0 || math.IsNaN(b) || math.IsInf(b, 0) {
		return 0, errf(CodeNoBudget, "the task's budget is %v USD: a budget of 0 means no spend, not unlimited, so it can't run. "+
			"Ask for a positive --budget-usd, or the operator sets one with `coder set --budget-usd`", b)
	}
	return b, nil
}

func (s *Service) checkUnattended(ctx context.Context, kind Kind, access string, unattended bool) error {
	if !unattended {
		return nil
	}
	if FullAccess(access) {
		return errf(CodeUnattendedFull, "--unattended needs a task that can't change anything (--access qa): "+
			"the %s level has full access to this computer, and nobody would be there to see it", access)
	}
	if !kind.UnattendedOK {
		return errf(CodeInvalidInput, "kind %s can't run unattended", kind.Name)
	}
	if !s.Launcher.Confined(ctx) {
		return errf(CodeUnattendedNoJail, "--unattended needs a profile whose tasks run in a verified confinement, and this one has none")
	}
	return nil
}

// Start runs a queued task's turn (the person's click for a queued one).
// Starting a task that is running or over returns it unchanged. Being queued
// is not permission to run: start re-checks that coder mode is still on, that
// the context folder and the capabilities are what they were at submit, and
// refuses (leaving the task queued) when they are not.
func (s *Service) Start(ctx context.Context, id string) (Task, error) {
	t, err := s.Store.Get(id)
	if err != nil || t.Status != StatusQueued {
		return t, err
	}
	if err := s.verifyAtStart(ctx, t); err != nil {
		return Task{}, err
	}
	if s.Admit != nil {
		if _, err := s.Admit(ctx, t); err != nil {
			return Task{}, err
		}
	}
	// The turn reads a verified, read-only copy, never the original folder.
	snapshot, err := s.SnapshotContext(id)
	if err != nil {
		return Task{}, err
	}
	if err := s.Store.SetPrompt(id, useSnapshot(t.Prompt, id, snapshot)); err != nil {
		return Task{}, err
	}
	token := NewToken()
	if err := s.Store.SetToken(id, token); err != nil {
		return Task{}, err
	}
	moved, err := s.Store.move(id, []string{StatusQueued}, StatusRunning, nil)
	if err != nil {
		return Task{}, err
	}
	if !moved {
		return s.Store.Get(id)
	}
	if err := s.Launcher.Detach(ctx, id, token); err != nil {
		_, _ = s.Store.move(id, []string{StatusRunning}, StatusFailed, map[string]any{"error": clip(err.Error()), "token_hash": ""})
		return Task{}, err
	}
	return s.Store.Get(id)
}

func (s *Service) verifyAtStart(ctx context.Context, t Task) error {
	if err := s.Launcher.Ready(ctx); err != nil {
		return err
	}
	kind, err := Lookup(t.Kind)
	if err != nil {
		return err
	}
	if err := s.checkUnattended(ctx, kind, t.Access, t.Unattended); err != nil {
		return err
	}
	if err := s.Launcher.Enforceable(kind.Name, t.Access); err != nil {
		return err
	}
	if t.BudgetUSD <= 0 {
		return errf(CodeNoBudget, "task %s has no budget: 0 means no spend, so it does not run; %s", t.ID, BudgetHint)
	}
	policy, err := s.Launcher.Policy(ctx)
	if err != nil {
		return err
	}
	if CapsHash(t, kind, policy) != t.CapsHash {
		return errf(CodeCapsChanged, "task %s was queued under other settings than are in force now (its access, kind or the coder settings changed): cancel it and submit it again", t.ID)
	}
	return nil
}

// Complete records result as task id's result. Only the holder of token may,
// and only for a task that is running or waiting for the user; a result that
// does not match the kind's schema is refused with the reason.
func (s *Service) Complete(id, token string, result []byte) (Task, error) {
	t, err := s.Store.Get(id)
	if err != nil {
		return Task{}, err
	}
	if !s.holds(id, token) {
		return Task{}, errf(CodeBadToken, "this process does not hold task %s's token: only the task's own session may complete it", id)
	}
	if t.Status != StatusRunning && t.Status != StatusNeedsUser {
		return Task{}, closedError(t, "complete it")
	}
	if len(result) > MaxResultBytes {
		return Task{}, errf(CodeSchemaInvalid, "the result is larger than %d MB", MaxResultBytes>>20)
	}
	var v any
	if err := json.Unmarshal(result, &v); err != nil {
		return Task{}, &Error{Code: CodeSchemaInvalid, Message: "the result file is not valid JSON: " + err.Error(),
			Fields: map[string]any{"reason": err.Error()}}
	}
	kind, err := Lookup(t.Kind)
	if err != nil {
		return Task{}, err
	}
	schema := kind.Schema
	if schema == nil {
		schema = map[string]any{"type": "object"}
	}
	if err := Validate(schema, v); err != nil {
		return Task{}, &Error{Code: CodeSchemaInvalid, Fields: map[string]any{"reason": err.Error()},
			Message: "the result does not match the " + kind.Name + " schema: " + err.Error() + ". Fix the file and run complete again."}
	}
	if err := runCheck(kind, s.Store.Profile, id, result); err != nil {
		return Task{}, &Error{Code: CodeSchemaInvalid, Fields: map[string]any{"reason": err.Error()},
			Message: "the result was refused by the " + kind.Name + " check: " + err.Error() + ". Fix the file and run complete again."}
	}
	// Secrets leave the result before it is stored, whatever the agent wrote.
	compact, _ := json.Marshal(RedactResult(v))
	if s.Redact != nil {
		compact = []byte(s.Redact(string(compact)))
	}
	moved, err := s.Store.move(id, []string{StatusRunning, StatusNeedsUser}, StatusComplete, map[string]any{"result_json": string(compact), "error": ""})
	if err != nil {
		return Task{}, err
	}
	if !moved {
		cur, _ := s.Store.Get(id)
		return Task{}, closedError(cur, "complete it")
	}
	return s.Store.Get(id)
}

// Fail marks task id failed for reason; the same holder rule as Complete.
func (s *Service) Fail(id, token, reason string) (Task, error) {
	t, err := s.Store.Get(id)
	if err != nil {
		return Task{}, err
	}
	if !s.holds(id, token) {
		return Task{}, errf(CodeBadToken, "this process does not hold task %s's token: only the task's own session may fail it", id)
	}
	if strings.TrimSpace(reason) == "" {
		return Task{}, errf(CodeInvalidInput, "--reason is required")
	}
	if s.Redact != nil {
		reason = s.Redact(reason)
	}
	moved, err := s.Store.move(id, []string{StatusRunning, StatusNeedsUser}, StatusFailed, map[string]any{"error": clip(reason)})
	if err != nil {
		return Task{}, err
	}
	if !moved {
		return Task{}, closedError(t, "fail it")
	}
	return s.Store.Get(id)
}

// Cancel stops a task that is not over. Its token stops working with it.
func (s *Service) Cancel(id string) (Task, error) {
	t, err := s.Store.Get(id)
	if err != nil {
		return Task{}, err
	}
	moved, err := s.Store.move(id, []string{StatusQueued, StatusRunning, StatusNeedsUser}, StatusCancelled, nil)
	if err != nil {
		return Task{}, err
	}
	if !moved {
		return Task{}, closedError(t, "cancel it")
	}
	return s.Store.Get(id)
}

// EndTurn records how a task's turn ended (see Store.EndTurn); the error is
// redacted first.
func (s *Service) EndTurn(id, turnErr string) error {
	if s.Redact != nil {
		turnErr = s.Redact(turnErr)
	}
	return s.Store.EndTurn(id, turnErr)
}

// Wait blocks until task id is over or needs the user, ctx ends, or timeout
// (when positive) passes. On a timeout it returns the task as it is with a
// CodeWaitTimeout error.
func (s *Service) Wait(ctx context.Context, id string, timeout time.Duration) (Task, error) {
	poll := s.PollEvery
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	var deadline <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	for {
		t, err := s.Store.Get(id)
		if err != nil || Terminal(t.Status) || t.Status == StatusNeedsUser {
			return t, err
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-deadline:
			return t, &Error{Code: CodeWaitTimeout, Message: "task " + id + " is still " + t.Status,
				Fields: map[string]any{"status": t.Status}}
		case <-time.After(poll):
		}
	}
}

func clip(s string) string {
	if len(s) <= maxErrorBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxErrorBytes], "") + "…"
}

// CheckTimeout bounds a kind's Check.
const CheckTimeout = 15 * time.Second

// runCheck runs the kind's Check, if any. A panic or a timeout is a refusal,
// never a pass.
func runCheck(k Kind, profile, taskID string, raw []byte) (err error) {
	if k.Check == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(WithTaskID(context.Background(), taskID), CheckTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("the check failed unexpectedly: %v", r)
			}
		}()
		done <- k.Check(ctx, profile, raw)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("the check timed out")
	}
}

type taskIDKey struct{}

// WithTaskID marks ctx as working for task id: Kind.Check gets such a context from
// Service.Complete, so a check can look at the task's own (immutable) context.
func WithTaskID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, taskIDKey{}, id)
}

// TaskIDFrom is the task id of ctx, or "".
func TaskIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(taskIDKey{}).(string)
	return id
}
