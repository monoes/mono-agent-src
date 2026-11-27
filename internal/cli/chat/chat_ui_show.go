package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/workflow"
)

// UIShowResult is what `chat ui show --json` prints.
type UIShowResult struct {
	ConversationID string          `json:"conversation_id"`
	Surfaces       []UIShowSurface `json:"surfaces"`
}

// UIShowSurface is one live surface as text. Every string is safe to print on
// a terminal (a2ui.CleanLine, a2ui.Text).
type UIShowSurface struct {
	SurfaceID  string `json:"surface_id"`
	Provenance string `json:"provenance"`
	AgentID    string `json:"agent_id,omitempty"`
	Text       string `json:"text,omitempty"`
	// Error says why a surface cannot be drawn.
	Error string `json:"error,omitempty"`
	// Decide holds, for a system surface, what each of its buttons stands for.
	Decide []UIShowDecision `json:"decide,omitempty"`
}

// UIShowDecision is one button of a system surface: the command that does the
// same, or a note when there is none.
type UIShowDecision struct {
	Action  string `json:"action"`
	Command string `json:"command,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Like `hil list`, which prints the same approvals, `chat ui show` is not
// operator-only: it decides nothing. A process that is not the user gets the
// reply links' tokens hidden, as `hil list` does. In client mode every chat
// command is refused (clientModeClasses).
func newChatUIShowCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "show <conversation-id> --profile <profile>",
		Short: "Print a conversation's live surfaces as text",
		Long: "Rebuilds the conversation's surfaces from its journal and prints each live one as text: its lines, the current " +
			"values of its inputs and its buttons with their action names. Lines of a surface start with \"| \". For a system " +
			"surface (an approval, a mission card, a worker's question) it also prints the command that decides it. " +
			"It changes nothing. A process that is not the user's own terminal sees reply links with their tokens hidden, " +
			"as with `hil list`.",
		Example: `  monoagentcli chat ui show c1 --profile work
  monoagentcli --json chat ui show c1 --profile work`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := runUIShow(cmd.Context(), cfg, args[0])
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return clicore.PrintJSON(res)
			}
			printUIShow(cmd.OutOrStdout(), res)
			return nil
		},
	}
}

func runUIShow(ctx context.Context, cfg *Deps, conversationID string) (*UIShowResult, error) {
	if ctx == nil {
		ctx = context.Background()
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
	redact, done, err := uiShowRedactor(ctx, cfg, profileID)
	if err != nil {
		return nil, err
	}
	defer done()
	replay := dynorg.RebuildRegistry(evs)
	res := &UIShowResult{ConversationID: conversationID, Surfaces: []UIShowSurface{}}
	for _, id := range liveInOrder(evs, replay) {
		s := replay.Registry.Surface(id)
		out := UIShowSurface{SurfaceID: a2ui.CleanLine(id), Provenance: string(s.Prov), AgentID: a2ui.CleanLine(replay.Authors[id])}
		text, err := a2ui.Text(s, a2ui.DefaultLimits)
		if err != nil {
			out.Error = a2ui.CleanLine(err.Error())
		} else {
			out.Text = redact(text)
			if s.Prov == a2ui.FromSystem {
				out.Decide = decisions(store, profileID, conversationID, s, refOfSurface(evs, id))
			}
		}
		res.Surfaces = append(res.Surfaces, out)
	}
	return res, nil
}

// uiShowRedactor hides reply tokens from a process that is not the user.
func uiShowRedactor(ctx context.Context, cfg *Deps, profileID string) (func(string) string, func(), error) {
	if clicore.OperatorRefusal(cfg.OperatorRefusal) == "" {
		return func(s string) string { return s }, func() {}, nil
	}
	db, err := cfg.OpenDB()
	if err != nil {
		return nil, nil, fmt.Errorf("initializing database: %w", err)
	}
	rows, err := db.DB.QueryContext(ctx, `SELECT DISTINCT execution_id FROM hil_pending WHERE profile_id = ?`, profileID)
	if err != nil {
		db.Close()
		return nil, nil, fmt.Errorf("listing the executions behind approvals: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			db.Close()
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		db.Close()
		return nil, nil, err
	}
	return workflow.NewReplyRedactor(ctx, db.DB, ids...).String, func() { db.Close() }, nil
}

// liveInOrder lists the live surfaces in the order they were last created.
func liveInOrder(evs []chatevents.Event, replay *dynorg.UIReplay) []string {
	var ids []string
	for _, ev := range evs {
		var p chatevents.UISurfacePayload
		if ev.Type != chatevents.EventUISurface || json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		m, err := a2ui.ParseMessage(p.Message, a2ui.DefaultLimits)
		if err != nil || m.Kind != a2ui.KindCreateSurface || replay.Registry.Surface(m.SurfaceID) == nil {
			continue
		}
		ids = append(slices.DeleteFunc(ids, func(s string) bool { return s == m.SurfaceID }), m.SurfaceID)
	}
	return ids
}

// decisions turns each button of a checked system surface into the command that
// does what it does. A button counts only when surfaces.Route accepts it for the
// record the surface was built from, so the ids printed are that record's.
func decisions(store *ai.AIStore, profileID, conversationID string, s *a2ui.Surface, ref chatevents.UIRef) []UIShowDecision {
	var out []UIShowDecision
	var walk func(id string)
	walk = func(id string) {
		c := s.Components[id]
		if child, ok := c["child"].(string); ok {
			walk(child)
		}
		kids, _ := c["children"].([]any)
		for _, k := range kids {
			if kid, ok := k.(string); ok {
				walk(kid)
			}
		}
		if c["component"] != "Button" {
			return
		}
		action, _ := c["action"].(map[string]any)
		ev, _ := action["event"].(map[string]any)
		name, _ := ev["name"].(string)
		given, _ := ev["context"].(map[string]any)
		literal := map[string]any{}
		for k, v := range given {
			if str, ok := v.(string); ok && (k == "id" || k == "sessionId" || k == "questionId") {
				literal[k] = str
			}
		}
		d := UIShowDecision{Action: a2ui.CleanLine(name)}
		cmd, err := surfaces.Route(a2ui.Action{Name: name, SurfaceID: s.ID, SourceComponentID: id, Context: literal}, ref)
		if err != nil {
			d.Note = "no command: " + a2ui.CleanLine(err.Error())
		} else {
			d.Command, d.Note = decisionCommand(store, profileID, conversationID, cmd, ref)
		}
		out = append(out, d)
	}
	walk("root")
	return out
}

// decisionCommand is the command line of one routed system action.
func decisionCommand(store *ai.AIStore, profileID, conversationID string, cmd surfaces.Command, ref chatevents.UIRef) (line, note string) {
	switch cmd.Kind {
	case "hil.approve":
		note = "add --data with a JSON object to change the editable fields"
		return cliLine(profileID, []string{"hil", "approve"}, nil, cmd.ID), note
	case "hil.reject":
		return cliLine(profileID, []string{"hil", "reject"}, nil, cmd.ID), ""
	case "mission.stop", "mission.close":
		return cliLine(profileID, []string{"task", "session", strings.TrimPrefix(cmd.Kind, "mission.")}, nil, cmd.ID), ""
	case "mission.edit":
		return "", "no command: tell the mission's chat what to change in mission #" + a2ui.CleanLine(cmd.ID)
	case "system.answer":
		agentID, qid, ok := splitAskID(cmd.ID)
		if !ok {
			return "", "no command: not a question id"
		}
		turnID, open, err := openQuestionOf(store, profileID, conversationID, agentID)
		if err != nil || open != qid {
			return "", "no command: the question is not open any more"
		}
		return cliLine(profileID, []string{"chat", "turn", "answer"},
			[]string{"--agent=" + agentID, "--question=" + qid, "--text=YOUR ANSWER"}, conversationID, turnID), "replace YOUR ANSWER"
	}
	return "", "no command for this action"
}

// plainArg is an argument a shell takes as it is.
var plainArg = regexp.MustCompile(`^[A-Za-z0-9_./:@%+,=-]+$`)

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	if plainArg.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cliLine builds a monoagentcli command line: verb, positional arguments, flags
// and the profile. An argument that holds a control character gives no line at
// all, and one that starts with a dash goes after "--" so it is not read as a flag.
func cliLine(profileID string, verb, flags []string, args ...string) string {
	for _, a := range append(append([]string{profileID}, flags...), args...) {
		if a2ui.CleanLine(a) != a {
			return ""
		}
	}
	dashed := slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "-") })
	parts := append([]string{"monoagentcli"}, verb...)
	tail := make([]string, 0, len(flags)+2)
	for _, f := range flags {
		tail = append(tail, shellQuote(f))
	}
	tail = append(tail, "--profile="+shellQuote(profileID))
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	if dashed {
		parts = append(append(append(parts, tail...), "--"), quoted...)
	} else {
		parts = append(append(parts, quoted...), tail...)
	}
	return strings.Join(parts, " ")
}

// printUIShow writes the result for a person. Every line of a surface's own text
// starts with "| ", so none of it can pass for the command lines under it.
func printUIShow(w io.Writer, res *UIShowResult) {
	if len(res.Surfaces) == 0 {
		fmt.Fprintln(w, "No live surfaces in this conversation.")
		return
	}
	for _, s := range res.Surfaces {
		by := s.Provenance
		if s.AgentID != "" {
			by += ", " + s.AgentID
		}
		fmt.Fprintf(w, "surface %s (%s)\n", s.SurfaceID, by)
		if s.Error != "" {
			fmt.Fprintf(w, "  cannot be drawn: %s\n", s.Error)
		}
		for _, l := range strings.Split(strings.TrimSuffix(s.Text, "\n"), "\n") {
			if s.Text != "" {
				fmt.Fprintf(w, "| %s\n", l)
			}
		}
		if len(s.Decide) > 0 {
			fmt.Fprintln(w, "decide with:")
		}
		for _, d := range s.Decide {
			switch {
			case d.Command == "":
				fmt.Fprintf(w, "  %s: %s\n", d.Action, d.Note)
			case d.Note != "":
				fmt.Fprintf(w, "  %s: %s\n    (%s)\n", d.Action, d.Command, d.Note)
			default:
				fmt.Fprintf(w, "  %s: %s\n", d.Action, d.Command)
			}
		}
		fmt.Fprintln(w)
	}
}
