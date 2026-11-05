package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgbridge"
	"github.com/monoes/mono-agent/internal/orgchat"
	"github.com/monoes/mono-agent/internal/orgdesign"
)

// Chatting with a running org's boss (#229). Tests replace these.
var (
	orgChatClient orgchat.Client = orgchat.MonomindClient{}
	orgChatLogs                  = monomind.OrgLogs
	orgChatSend                  = orgbridge.Send
)

// orgChatRef is what a question id, gate id, request id or role:action
// looks like; anything else (a flag, a path) is refused before monomind
// sees it.
var orgChatRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]*$`)

// tail is the text after the n leading positionals. "--" may come before
// them (the app always puts it first) or after (`answer <org> <id> -- …`);
// written in both places, the second one is a separator too.
func tail(cmd *cobra.Command, args []string, n int) string {
	rest := args[n:]
	if at := cmd.ArgsLenAtDash(); at >= 0 && at < n && len(rest) > 0 && rest[0] == "--" {
		rest = rest[1:]
	}
	return strings.TrimSpace(joinRemainder(rest))
}

// orgChatSender is who the person's chat messages come from.
const orgChatSender = "human:operator"

// orgChatSubject marks a message as the person's, typed in the chat.
const orgChatSubject = "Chat from the operator"

func newOrgChatCmd(env *orgEnv) *cobra.Command {
	c := &cobra.Command{
		Use:   "chat",
		Short: "Chat with a running org's boss: send, read the thread, answer questions and gates",
		Long: "The boss thread of an org: what you sent, what the boss said, the questions, approvals and gates " +
			"the org raised (answered here), and role-to-role messages as rows. answer, approve and deny are " +
			"idempotent: an item already resolved reports \"already\": true and nothing is sent. They refuse " +
			"(exit 3) while the org is not running, so nothing is lost; the item stays pending.",
	}
	c.AddCommand(newOrgChatSendCmd(env), newOrgChatHistoryCmd(env), newOrgChatAnswerCmd(env),
		newOrgChatDismissCmd(env), newOrgChatResolveCmd(env, "approve", true), newOrgChatResolveCmd(env, "deny", false))
	return c
}

func newOrgChatSendCmd(env *orgEnv) *cobra.Command {
	var to string
	c := &cobra.Command{
		Use:   "send <org> -- <text...>",
		Short: "Send the boss a message (live when the org runs, queued for its next start otherwise)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			if !orgdesign.ValidOrgName(org) {
				return env.d.InvalidInput("invalid org name %q", org)
			}
			text := tail(cmd, args, 1)
			if text == "" {
				return env.d.InvalidInput("the message is empty")
			}
			db, profileID, root, err := env.Profile()
			if err != nil {
				return err
			}
			if _, err := orgdesign.Load(root, org); err != nil {
				return env.d.NotFound("org %q not found", org)
			}
			res, err := orgChatSend(cmd.Context(), orgbridge.NewLedger(db.DB), orgbridge.SendRequest{
				ProfileID: profileID, Root: root, Org: org, To: to, From: orgChatSender, Subject: orgChatSubject,
				Body: text, Direction: orgbridge.DirWorkflowOut,
			})
			if err != nil {
				var refused *orgbridge.ErrRefused
				if errors.As(err, &refused) {
					return env.d.InvalidInput("%s", refused.Error())
				}
				return err
			}
			return printJSONValue(map[string]interface{}{
				"v": 1, "org": org, "to": res.To, "delivery": res.Delivery, "messageId": res.MessageID,
			})
		},
	}
	c.Flags().StringVar(&to, "to", "", "Role id (default: the org's boss)")
	return c
}

// orgChatRole is a role of the org as the chat's stage shows it.
type orgChatRole struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Type      string  `json:"type"`
	ReportsTo *string `json:"reports_to"`
	Runtime   string  `json:"runtime,omitempty"`
	Model     string  `json:"model,omitempty"`
}

func extraString(extra map[string]json.RawMessage, key string) string {
	var s string
	if raw, ok := extra[key]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func newOrgChatHistoryCmd(env *orgEnv) *cobra.Command {
	var run string
	var limit int
	c := &cobra.Command{
		Use:   "history <org>",
		Short: "The boss thread: messages, the boss's replies, questions, approvals and gates (JSON)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org := args[0]
			if !orgdesign.ValidOrgName(org) {
				return env.d.InvalidInput("invalid org name %q", org)
			}
			if limit < 0 {
				return env.d.InvalidInput("--limit must be 0 or more")
			}
			root, err := env.RootErr()
			if err != nil {
				return err
			}
			doc, err := orgdesign.Load(root, org)
			if err != nil {
				return env.d.NotFound("org %q not found", org)
			}
			boss, bossTitle := "", ""
			if r, ok := doc.RootRole(); ok {
				boss, bossTitle = r.ID, r.Title
			}
			roles := make([]orgChatRole, 0, len(doc.Roles))
			for _, r := range doc.Roles {
				roles = append(roles, orgChatRole{ID: r.ID, Title: r.Title, Type: r.Type, ReportsTo: r.ReportsTo,
					Runtime: extraString(r.Extra, "runtime"), Model: extraString(r.Extra, "model")})
			}
			h := fetchOrgChat(cmd.Context(), root, org, run)
			items := orgchat.BuildThread(org, boss, h.events, h.human, limit)
			pending := map[string]int{"questions": 0, "approvals": 0, "gates": 0}
			for _, it := range items {
				if it.Pending {
					pending[it.Kind+"s"]++
				}
			}
			out := map[string]interface{}{
				"v": 1, "org": org, "boss": boss, "boss_title": bossTitle, "roles": roles,
				"status": h.status.Status, "paused": h.status.Paused, "run": h.status.Run,
				"items": items, "pending": pending, "warnings": h.warnings,
			}
			// A crashed run says why ("pid 123 gone", monomind 2.21+).
			if h.status.Error != "" {
				out["status_error"] = h.status.Error
			}
			return printJSONValue(out)
		},
	}
	c.Flags().StringVar(&run, "run", "", "Specific run id (default: the most recent run)")
	c.Flags().IntVar(&limit, "limit", 200, "Keep the newest N items (0 = all); pending items are always kept")
	return c
}

type orgChatState struct {
	events []orgchat.Event
	human  orgchat.HumanItems
	status struct {
		Status string `json:"status"`
		Paused bool   `json:"paused"`
		Run    string `json:"run"`
		Error  string `json:"error"`
	}
	warnings []string
}

// fetchOrgChat reads the bus log, the three item lists and the status at
// once. A part that fails is a warning: the rest of the thread still shows
// (an org that never ran has no log at all).
func fetchOrgChat(ctx context.Context, root, org, run string) *orgChatState {
	st := &orgChatState{warnings: []string{}}
	var mu sync.Mutex
	warn := func(what string, err error) {
		mu.Lock()
		st.warnings = append(st.warnings, what+": "+err.Error())
		mu.Unlock()
	}
	var wg sync.WaitGroup
	do := func(f func()) {
		wg.Add(1)
		go func() { defer wg.Done(); f() }()
	}
	do(func() {
		raw, err := orgChatLogs(ctx, root, org, run)
		if err == nil {
			st.events, err = orgchat.ParseLogs(raw)
		}
		if err != nil {
			warn("logs", err)
		}
	})
	do(func() {
		raw, err := orgChatClient.All(ctx, root, org, "questions")
		if err == nil {
			st.human.Questions, err = orgchat.ParseQuestions(raw)
		}
		if err != nil {
			warn("questions", err)
		}
	})
	do(func() {
		raw, err := orgChatClient.All(ctx, root, org, "approvals")
		if err == nil {
			st.human.Approvals, err = orgchat.ParseApprovals(raw)
		}
		if err != nil {
			warn("approvals", err)
		}
	})
	do(func() {
		raw, err := orgChatClient.All(ctx, root, org, "gates")
		if err == nil {
			st.human.Gates, err = orgchat.ParseGates(raw)
		}
		if err != nil {
			warn("gates", err)
		}
	})
	do(func() {
		raw, err := orgChatClient.Status(ctx, root, org)
		if err == nil {
			err = json.Unmarshal(raw, &st.status)
		}
		if err != nil {
			warn("status", err)
		}
	})
	wg.Wait()
	return st
}

func runOrgChatResolve(cmd *cobra.Command, env *orgEnv, req orgchat.Request) error {
	if !orgdesign.ValidOrgName(req.Org) {
		return env.d.InvalidInput("invalid org name %q", req.Org)
	}
	if !orgChatRef.MatchString(req.Ref) {
		return env.d.InvalidInput("invalid item ref %q", req.Ref)
	}
	root, err := env.RootErr()
	if err != nil {
		return err
	}
	if req.Approve && strings.HasPrefix(req.Ref, "gate-") {
		// Approving a gate is approving an approval: the user's (class B). An approval ref is checked
		// by Allow, which says more when the approval sends.
		if err := env.refuseNotOperator(fmt.Sprintf("approving %s in org %s", req.Ref, req.Org)); err != nil {
			return err
		}
	}
	if req.Approve {
		req.Allow = func(action string) error { return env.refuseChatApproval(cmd.Context(), req.Org, action) }
	}
	res, err := orgchat.Resolve(cmd.Context(), orgChatClient, root, req)
	if err != nil {
		var stopped *orgchat.NotRunningError
		switch {
		case errors.Is(err, orgchat.ErrNotFound):
			return env.d.NotFound("org %q has no question, approval or gate %q", req.Org, req.Ref)
		case errors.As(err, &stopped):
			return env.d.InvalidInput("%s", stopped.Error())
		}
		return err
	}
	return printJSONValue(res)
}

func newOrgChatAnswerCmd(env *orgEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "answer <org> <questionId> -- <answer...>",
		Short: "Answer an org's question (idempotent; refused while the org is not running)",
		Args:  cobra.MinimumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			text := tail(cmd, args, 2)
			if text == "" {
				return env.d.InvalidInput("the answer is empty")
			}
			return runOrgChatResolve(cmd, env, orgchat.Request{Org: args[0], Ref: args[1], Answer: true, Text: text})
		},
	}
}

func newOrgChatDismissCmd(env *orgEnv) *cobra.Command {
	var reason string
	c := &cobra.Command{
		Use:   "dismiss <org> <questionId> [--reason <text>]",
		Short: "Close an org's question without answering it (idempotent; refused while the org is not running)",
		Long: "Closes a question nobody will answer, so a blocking one stops holding the org back; the asking " +
			"role is told. A question already answered or dismissed reports \"already\": true and nothing is sent.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOrgChatResolve(cmd, env, orgchat.Request{Org: args[0], Ref: args[1], Dismiss: true, Text: reason})
		},
	}
	c.Flags().StringVar(&reason, "reason", "", "Why it is dismissed (shown to the role)")
	return c
}

func newOrgChatResolveCmd(env *orgEnv, verb string, approve bool) *cobra.Command {
	short := "Approve an approval request or a gate (idempotent; refused while the org is not running)"
	if !approve {
		short = "Deny an approval request or reject a gate (idempotent; refused while the org is not running)"
	}
	return &cobra.Command{
		Use: verb + " <org> <ref> [-- note...]",
		Long: short + ". ref is a gate id (gate-…), an approval's request id, role:action:ts, or role:action " +
			"(its pending request). A note is kept as a gate's resolution; approvals take none. Approving an approval " +
			"(not a gate) is the operator's only: an agent, the chat, a workflow or a coder turn is refused; denying stays open. " +
			"Approving a gate is the operator's only too.",
		Short: short,
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOrgChatResolve(cmd, env, orgchat.Request{
				Org: args[0], Ref: args[1], Approve: approve, Text: tail(cmd, args, 2),
			})
		},
	}
}
