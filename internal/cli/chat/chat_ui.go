package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
)

// uiConfig turns on the ui_render tool of a conversation's dynamic org; it
// loads the surfaces of earlier turns lazily, on the first ui_render.
func uiConfig(db *sql.DB, profileID, conversationID string) *dynorg.UIConfig {
	return &dynorg.UIConfig{Seed: func() ([]chatevents.Event, error) {
		store, err := ai.NewAIStore(db)
		if err != nil {
			return nil, err
		}
		return store.UIEvents(profileID, conversationID)
	}}
}

// SystemActionHandler runs a button press of a system surface (names that start
// with hil., mission. or system.). The surface is the live one the press was
// checked against. The `chat ui action` command never runs such an action
// itself: it calls the handler registered for the name's prefix, if any.
type SystemActionHandler func(ctx context.Context, a a2ui.Action, s *a2ui.Surface) error

// SystemActions holds the system action handlers by name prefix.
type SystemActions struct {
	mu       sync.Mutex
	handlers map[string]SystemActionHandler
}

// DefaultSystemActions is the registry the CLI's `chat ui action` uses.
var DefaultSystemActions = &SystemActions{}

// Register sets the handler of the action names that start with prefix, one of
// a2ui.ReservedActionPrefixes. A second registration of a prefix replaces the first.
func (r *SystemActions) Register(prefix string, h SystemActionHandler) error {
	for _, p := range a2ui.ReservedActionPrefixes {
		if p == prefix {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.handlers == nil {
				r.handlers = map[string]SystemActionHandler{}
			}
			r.handlers[prefix] = h
			return nil
		}
	}
	return fmt.Errorf("%q is not a reserved action prefix (%s)", prefix, strings.Join(a2ui.ReservedActionPrefixes, " "))
}

// RegisterSystemActionHandler registers h on DefaultSystemActions.
func RegisterSystemActionHandler(prefix string, h SystemActionHandler) error {
	return DefaultSystemActions.Register(prefix, h)
}

// prefixOf is the reserved prefix of an action name, "" for an agent's.
func prefixOf(name string) string {
	for _, p := range a2ui.ReservedActionPrefixes {
		if strings.HasPrefix(name, p) {
			return p
		}
	}
	return ""
}

func (r *SystemActions) handlerFor(prefix string) SystemActionHandler {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.handlers[prefix]
}

func newChatUICmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "ui", Short: "Surfaces an agent showed in a conversation (A2UI)"}
	registerSystemHandlers(cfg, DefaultSystemActions)
	cmd.AddCommand(newChatUIActionCmd(cfg, DefaultSystemActions), newChatUISyncHILCmd(cfg), newChatUIShowCmd(cfg))
	return cmd
}

func newChatUIActionCmd(cfg *Deps, sys *SystemActions) *cobra.Command {
	var raw string
	cmd := &cobra.Command{
		Use:   "action <conversation-id> --action <envelope|->",
		Short: "Send the user's button press on a surface back",
		Long: "Takes the renderer's A2UI action envelope {\"version\":\"1.0\",\"action\":{name, surfaceId, sourceComponentId, context, ...}}, " +
			"checks it against the conversation's live surfaces (rebuilt from its journal: the surface must be live and its " +
			"button must have declared exactly that name), and journals a ui.action event with the outcome accepted, " +
			"rejected or stale. The result is always one JSON document with the outcome (exit 0).\n\n" +
			"When several cards of one reply wait for the user (workers' questions, or agent cards with buttons), a press " +
			"of one alone is rejected: send them together as {\"version\":\"1.0\",\"actions\":[<action>,...],\"skipped\":[<surfaceId>,...]}. " +
			"Every waiting card is either in actions or, for an agent's card only, in skipped; a set is delivered whole or not at all, " +
			"each agent card's action names the card's \"revision\" (\"<turn>:<seq>\" of its last ui.surface event; a single action " +
			"may name it too, and a changed card refuses it), the next-turn message holds at most 16384 bytes (with \"appendBytes\", " +
			"what the caller adds to it), " +
			"and never holds an approval or a mission button.\n\n" +
			"An accepted press on an agent's surface is delivered to that agent: as the answer to its open question when the " +
			"agent is waiting in a running turn (\"delivered\":\"answer\"), otherwise the document carries \"message\", the text " +
			"the caller sends as the conversation's next user message (\"delivered\":\"next_turn\"). A press on a system surface " +
			"(hil., mission., system.) is never run here: it goes to the handler registered for its prefix, if any. " +
			clicore.OperatorOnlyHelp,
		Example: `  monoagentcli chat ui action c1 --profile work --action '{"version":"1.0","action":{"name":"submit_form","surfaceId":"s1","sourceComponentId":"ok","context":{}}}'`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// requireUser asks the turn-aware refusal first, so a process the gate cannot place is refused as
			// operator_only rather than with a database error.
			if err := requireUser(cfg, "press a button on a surface"); err != nil {
				return err
			}
			env, err := readUIAction(raw, cmd.InOrStdin())
			if err != nil {
				return err
			}
			res, err := runUIAction(cmd.Context(), cfg, sys, args[0], env)
			if err != nil {
				return err
			}
			return clicore.PrintJSON(res)
		},
	}
	// Not --json: that is the global output flag, which callers put before the subcommand.
	cmd.Flags().StringVar(&raw, "action", "", "The action envelope, or - to read it from stdin (which keeps it out of process listings)")
	_ = cmd.MarkFlagRequired("action")
	return cmd
}

// readUIAction returns the envelope: the flag's value, or stdin for "-". It reads one byte past the cap so that
// ParsePress refuses an oversized envelope instead of a cut one.
func readUIAction(flag string, stdin io.Reader) ([]byte, error) {
	if flag != "-" {
		return []byte(flag), nil
	}
	b, err := io.ReadAll(io.LimitReader(stdin, int64(a2ui.DefaultLimits.MaxSetBytes)+1))
	if err != nil {
		return nil, clicore.InvalidInput("read the action from stdin: %v", err)
	}
	return b, nil
}

// UIActionResult is what `chat ui action` prints.
type UIActionResult struct {
	Outcome   string `json:"outcome"`
	Reason    string `json:"reason,omitempty"`
	Delivered string `json:"delivered,omitempty"` // answer, next_turn, system
	Handled   bool   `json:"handled,omitempty"`   // a system handler ran
	Message   string `json:"message,omitempty"`   // for delivered next_turn: the text to send as the user's message
	AgentID   string `json:"agent_id,omitempty"`
}

const (
	outcomeAccepted = "accepted"
	outcomeRejected = "rejected"
	outcomeStale    = "stale"
)

func runUIAction(ctx context.Context, cfg *Deps, sys *SystemActions, conversationID string, raw []byte) (*UIActionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	press, err := a2ui.ParsePress(raw, a2ui.DefaultLimits)
	if err != nil {
		return nil, clicore.InvalidInput("%v", err)
	}
	store, profileID, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	if _, err := store.GetConversation(conversationID, profileID); err != nil {
		return nil, chatStoreErr(err)
	}
	evs, err := store.UIEvents(profileID, conversationID)
	if err != nil {
		return nil, err
	}
	ctx = withUIConv(ctx, conversationID, profileID)
	replay := dynorg.RebuildRegistry(evs)
	if press.Set {
		return runUISet(store, profileID, conversationID, press, evs, replay)
	}
	act := press.Actions[0]
	record := func(res *UIActionResult) (*UIActionResult, error) {
		_, err := store.RecordUIAction(profileID, conversationID, chatevents.UIActionPayload{
			SurfaceID: act.SurfaceID, Name: act.Name, SourceComponentID: act.SourceComponentID,
			Context: act.Context, Outcome: res.Outcome, Reason: res.Reason,
		})
		return res, err
	}
	surf, err := replay.Registry.CheckAction(act)
	if err != nil {
		outcome := outcomeRejected
		if replay.Closed[act.SurfaceID] {
			outcome = outcomeStale
		}
		return record(&UIActionResult{Outcome: outcome, Reason: err.Error()})
	}
	if why := changedCard(act, surf, replay); why != "" {
		return record(&UIActionResult{Outcome: outcomeRejected, Reason: why})
	}
	waiting, err := waitingCards(store, profileID, conversationID, replay)
	if err != nil {
		return nil, err
	}
	if n := waiting.with(act.SurfaceID); n > 1 {
		return record(&UIActionResult{Outcome: outcomeRejected, Reason: fmt.Sprintf("%d cards are waiting for you: answer all of them and send them together", n)})
	}
	if prefix := prefixOf(act.Name); surf.Prov == a2ui.FromSystem || prefix != "" {
		return systemAction(ctx, sys, act, surf, prefix, record)
	}
	return agentAction(store, profileID, conversationID, act, actionMessage(act, surf, surfaceHistory(evs, act.SurfaceID)), replay.Authors[act.SurfaceID], replay.Turns[act.SurfaceID], record)
}

// systemAction never runs the action: it hands it to the prefix's handler. The
// press is journaled accepted once the handler (if any) took it, rejected when
// it refused.
func systemAction(ctx context.Context, sys *SystemActions, act a2ui.Action, surf *a2ui.Surface, prefix string, record func(*UIActionResult) (*UIActionResult, error)) (*UIActionResult, error) {
	if prefix == "" || surf.Prov != a2ui.FromSystem {
		return record(&UIActionResult{Outcome: outcomeRejected, Reason: fmt.Sprintf("action %q does not belong to a system surface's reserved prefix", act.Name)})
	}
	res := &UIActionResult{Outcome: outcomeAccepted, Delivered: "system"}
	if h := sys.handlerFor(prefix); h != nil {
		if err := h(ctx, act, surf); err != nil {
			return record(&UIActionResult{Outcome: outcomeRejected, Reason: err.Error()})
		}
		res.Handled = true
	}
	if conv, ok := uiConvOf(ctx); ok && conv.out.Message != "" {
		res.Delivered, res.Message = "next_turn", conv.out.Message
	}
	return record(res)
}

// agentAction delivers an accepted press on an agent's surface. Route: the answer
// mechanism when the author is a worker waiting on a question in a running turn
// (the turn passes the answer on within a second), otherwise the next user
// message, which the caller sends (this process cannot write a running turn's
// journal, and a finished conversation has nothing waiting).
func agentAction(store *ai.AIStore, profileID, conversationID string, act a2ui.Action, text, author, cardTurn string, record func(*UIActionResult) (*UIActionResult, error)) (*UIActionResult, error) {
	res := &UIActionResult{Outcome: outcomeAccepted, Delivered: "next_turn", Message: text, AgentID: author}
	if author != "" {
		turnID, qid, err := openQuestionOf(store, profileID, conversationID, author)
		if err != nil {
			return nil, err
		}
		// Only the card's own turn: a later turn may reuse the worker id.
		if qid != "" && turnID == cardTurn {
			if err := store.AddAnswer(profileID, conversationID, turnID, author, qid, text); err == nil {
				res.Delivered, res.Message = "answer", ""
			}
		}
	}
	if why := tooLong(len(res.Message)); why != "" {
		return record(&UIActionResult{Outcome: outcomeRejected, Reason: why, AgentID: author})
	}
	return record(res)
}

// changedCard refuses a press made on an earlier version of an agent's card: its
// agent can change what a button says and does. "" lets the press go on (the
// host's approval cards carry a digest of their own, and a caller that names no
// version answers the card as it is now).
func changedCard(a a2ui.Action, s *a2ui.Surface, replay *dynorg.UIReplay) string {
	if s.Prov == a2ui.FromAgent && a.Revision != "" && a.Revision != replay.Revisions[a.SurfaceID] {
		return "the card changed after you answered it: look at it again, then answer"
	}
	return ""
}

// tooLong refuses a next-turn message a chat cannot take whole.
func tooLong(bytes int) string {
	if bytes <= a2ui.MaxNextTurnBytes {
		return ""
	}
	return fmt.Sprintf("these answers make a message of %d bytes and a chat message holds %d: shorten what you typed, or skip a card",
		bytes, a2ui.MaxNextTurnBytes)
}

// openQuestionOf finds the open question of a worker in the conversation's
// running turn: the turn and question ids, "" when it has none.
func openQuestionOf(store *ai.AIStore, profileID, conversationID, agentID string) (turnID, questionID string, err error) {
	turn, open, err := openQuestions(store, profileID, conversationID)
	if ids := open[agentID]; err == nil && len(ids) > 0 {
		return turn, ids[len(ids)-1], nil
	}
	return "", "", err
}

// openQuestions lists the questions nothing has closed yet in the
// conversation's running turn: the turn id and, per worker, the question ids.
func openQuestions(store *ai.AIStore, profileID, conversationID string) (string, map[string][]string, error) {
	t, err := store.ActiveTurn(conversationID, profileID)
	if errors.Is(err, ai.ErrTurnNotFound) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	open := map[[2]string]bool{}
	var after int64
	for {
		evs, err := store.GetEvents(conversationID, t.ID, profileID, after, 500)
		if err != nil {
			return "", nil, err
		}
		for _, ev := range evs {
			after = ev.Seq
			var m chatevents.AgentMessagePayload
			if ev.Type != chatevents.EventAgentMessage || json.Unmarshal(ev.Payload, &m) != nil || m.QuestionID == "" {
				continue
			}
			switch key := [2]string{m.AgentID, m.QuestionID}; {
			case m.Direction == "question":
				open[key] = true
			case m.Direction == "followup" && (m.From == "user" || m.From == "system"):
				delete(open, key)
			}
		}
		if len(evs) < 500 {
			break
		}
	}
	byAgent := map[string][]string{}
	for k := range open {
		byAgent[k[0]] = append(byAgent[k[0]], k[1])
	}
	for _, ids := range byAgent {
		sort.Strings(ids)
	}
	return t.ID, byAgent, nil
}
