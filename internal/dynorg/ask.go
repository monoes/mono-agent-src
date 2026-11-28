package dynorg

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// A worker asking the user a question (#256). A worker whose exec can take
// caller tools gets ask_user. The conductor journals the question
// (agent.status waiting_user, then agent.message direction "question"),
// lets go of the worker's leases and slot while it waits, and polls
// Config.Answers until the user answers (`chat turn answer`), the question
// times out, or the worker is stopped. Every way a question ends is
// journaled as a followup with its questionId, from "user" or "system".
//
// Limits of the lease hand-off: background processes the worker started
// keep running while it waits, and a tool call it makes in the same
// message as ask_user isn't held back by the lease it gave up.

// ToolAskUser is the worker's question tool.
const ToolAskUser = "ask_user"

// MaxQuestions caps a worker's questions per run, however they are spread over calls.
const MaxQuestions = 3

// AskTimeout bounds one question; unanswered, the worker is told to go on
// with its best judgment.
var AskTimeout = 10 * time.Minute

// askPoll is how often an open question checks for an answer.
var askPoll = time.Second

// AnswerSource hands the conductor the user's answer to a question once it
// has arrived.
type AnswerSource interface {
	Answer(ctx context.Context, agentID, questionID string) (text string, ok bool, err error)
}

const askRule = "- If a decision only the user can make blocks you, call ask_user on its own, not alongside " +
	"other tool calls, and wait for the answer; otherwise decide yourself and say what you assumed. Put every " +
	"question you have in that one call (questions, at most 3 per task): the user answers them together.\n"

func askSpec() monomind.ToolSpec {
	return monomind.ToolSpec{
		Name: ToolAskUser,
		Description: "Ask the user and wait for the answer. Use it only for decisions you can't make yourself. " +
			"Put all your questions in one call: the user answers them together.",
		Schema: obj(map[string]any{"questions": map[string]any{
			"type": "array", "description": "The questions, each one or two sentences.",
			"items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": MaxQuestions,
		}}, "questions"),
	}
}

// askArgs reads an ask_user call's questions. "question" is the single
// question older prompts send.
func askArgs(args json.RawMessage) ([]string, error) {
	var a struct {
		Question  string   `json:"question"`
		Questions []string `json:"questions"`
	}
	var out []string
	if err := json.Unmarshal(orEmpty(args), &a); err == nil {
		for _, q := range append([]string{a.Question}, a.Questions...) {
			if q = strings.TrimSpace(q); q != "" {
				out = append(out, q)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("questions is required: a list of 1 to %d questions", MaxQuestions)
	}
	return out, nil
}

// workerTools gives the worker its caller tools when its exec can take
// them in its access mode: ask_user when answers can come back, and the
// sub-worker tools when the lead let it spawn (tree.go); otherwise none.
func (c *Conductor) workerTools(w *worker, m Model, opts *monomind.ExecOptions) {
	opts.Tools, opts.OnToolCall = nil, nil
	if !canTakeTools(m, opts.Access) {
		return
	}
	asks, spawns, ui := c.cfg.Answers != nil, c.spawnsOn(w, m, opts.Access), c.cfg.UI != nil
	if !asks && !spawns && !ui {
		return
	}
	if asks {
		opts.Tools = append(opts.Tools, askSpec())
		opts.ToolTimeout = AskTimeout + time.Minute
		opts.SystemPrompt += askRule
	}
	if spawns {
		opts.Tools = append(opts.Tools, childToolSpecs(c.cfg.Staffer.profiles())...)
		opts.ToolTimeout = max(opts.ToolTimeout, ToolTimeout)
	}
	if ui {
		opts.Tools = append(opts.Tools, UIToolSpecs()...)
		opts.ToolTimeout = max(opts.ToolTimeout, time.Minute)
		opts.SystemPrompt += a2ui.PromptSnippet()
	}
	opts.OnToolCall = func(ctx context.Context, name string, args json.RawMessage) (string, error) {
		if ui && name == ToolUIRender {
			return c.handle(ctx, w, name, args)
		}
		if spawns && (name == ToolSpawn || name == ToolWait || name == ToolMessage) {
			return c.handle(ctx, w, name, args)
		}
		if !asks || name != ToolAskUser {
			return "", fmt.Errorf("unknown tool %q", name)
		}
		questions, err := askArgs(args)
		if err != nil {
			return "", err
		}
		return c.ask(ctx, w, questions)
	}
	if m.Runtime == "claude" {
		// Claude Code times out MCP calls on its own; let the wait run.
		env := maps.Clone(opts.Env)
		if env == nil {
			env = map[string]string{}
		}
		env["MCP_TOOL_TIMEOUT"] = strconv.FormatInt(opts.ToolTimeout.Milliseconds(), 10)
		opts.Env = env
	}
}

// ask journals a worker's questions as one question of the journal and one
// card, and waits for the user's answer to all of them.
func (c *Conductor) ask(ctx context.Context, w *worker, questions []string) (string, error) {
	question := questions[0]
	if len(questions) > 1 {
		numbered := make([]string, len(questions))
		for i, q := range questions {
			numbered[i] = strconv.Itoa(i+1) + ". " + q
		}
		question = strings.Join(numbered, "\n")
	}
	c.mu.Lock()
	// One question at a time: tool calls of one message run concurrently,
	// and a second ask would release nothing and retake a second slot.
	if w.openQuestion != "" {
		c.mu.Unlock()
		return "", fmt.Errorf("you already have an open question for the user; wait for its answer before asking another " +
			"(put all your questions in one call)")
	}
	if left := MaxQuestions - w.askedThisRun; len(questions) > left {
		asked := w.askedThisRun
		c.mu.Unlock()
		if left > 0 {
			return "", fmt.Errorf("you can ask %d more question(s) in this task; ask only what matters most, in one call", left)
		}
		return "", fmt.Errorf("you already asked %d questions; decide yourself and say what you assumed", asked)
	}
	w.askedThisRun += len(questions)
	w.questionSeq++
	qid := "q" + strconv.Itoa(w.questionSeq)
	w.openQuestion = question // claimed here, in the same critical section as the check
	c.mu.Unlock()

	// While it waits it holds nothing: its leases and its concurrency slot
	// go to others, and it takes them back in the conductor's order
	// (leases, then the slot), so no one can deadlock against it.
	c.suspend(w)
	// Status first: the stage clears "needs you" on the next status change.
	c.setStatus(w, chatevents.AgentWaitingUser, qid)
	bounded, cut, _ := chatevents.BoundText(question, 2000*len(questions))
	c.cfg.Emit.Emit(chatevents.EventAgentMessage, chatevents.AgentMessagePayload{
		AgentID: w.id, Direction: "question", QuestionID: qid, From: w.id, To: "user", Text: bounded, Truncated: cut,
	})
	c.askSurface(w.id, qid, questions...)

	answer, answered, err := c.waitAnswer(ctx, w.id, qid)
	c.mu.Lock()
	w.openQuestion = ""
	c.mu.Unlock()
	switch {
	case answered:
		c.emitAnswer(w.id, qid, "user", answer)
	case err != nil:
		// Stopped while waiting: close the question so a late answer is
		// refused (`chat turn answer` checks the journal).
		c.emitAnswer(w.id, qid, "system", "The worker was stopped before the question was answered.")
		return "", err
	default:
		c.emitAnswer(w.id, qid, "system", fmt.Sprintf("No answer within %s; the worker went on without one.", AskTimeout))
	}
	if _, err := c.unsuspend(ctx, w); err != nil {
		return "", err
	}
	c.setStatus(w, chatevents.AgentWorking, "")
	if !answered {
		return fmt.Sprintf("The user didn't answer within %s. Go on with your best judgment and say in your report what you assumed.", AskTimeout), nil
	}
	return "The user answered: " + answer, nil
}

// emitAnswer closes a question in the journal: the user's answer, or
// (from "system") why it closed without one.
func (c *Conductor) emitAnswer(agentID, qid, from, text string) {
	bounded, cut, _ := chatevents.BoundText(text, maxReport)
	c.cfg.Emit.Emit(chatevents.EventAgentMessage, chatevents.AgentMessagePayload{
		AgentID: agentID, Direction: "followup", QuestionID: qid, From: from, To: agentID, Text: bounded, Truncated: cut,
	})
	if from == "user" {
		c.closeAskSurface(agentID, qid, "Answered")
	} else {
		c.closeAskSurface(agentID, qid, "Closed without an answer")
	}
}

// waitAnswer polls for the answer until it arrives, AskTimeout passes, or
// ctx ends (the turn stopped, or org_stop).
func (c *Conductor) waitAnswer(ctx context.Context, agentID, qid string) (string, bool, error) {
	deadline := time.NewTimer(AskTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(askPoll)
	defer tick.Stop()
	for {
		if text, ok, err := c.cfg.Answers.Answer(ctx, agentID, qid); err == nil && ok {
			return text, true, nil
		}
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-deadline.C:
			return "", false, nil
		case <-tick.C:
		}
	}
}

// releaseHeld lets go of the leases and the concurrency slot w holds and
// returns the leases. The journal reports the hand-off (agent.status with
// the leases it holds now), so the stage shows the pen changing hands.
func (c *Conductor) releaseHeld(w *worker) []heldLease {
	c.mu.Lock()
	held := w.leases
	w.leases = nil
	c.reportLocked(w)
	c.mu.Unlock()
	for _, h := range held {
		h.l.release()
	}
	c.dropSlot(w)
	return held
}

// retakeHeld takes the leases back in their original order, then a slot:
// the same order runLeased takes them in.
func (c *Conductor) retakeHeld(ctx context.Context, w *worker, held []heldLease) error {
	for _, h := range held {
		if !h.l.tryAcquire() {
			c.setStatus(w, chatevents.AgentWaitingLease, h.name)
			if err := h.l.acquire(ctx); err != nil {
				return err
			}
		}
		c.mu.Lock()
		w.leases = append(w.leases, h)
		c.reportLocked(w)
		c.mu.Unlock()
	}
	select {
	case c.slots <- struct{}{}:
	default:
		c.setStatus(w, chatevents.AgentQueued, "slot")
		select {
		case c.slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.mu.Lock()
	w.hasSlot = true
	c.mu.Unlock()
	return nil
}
