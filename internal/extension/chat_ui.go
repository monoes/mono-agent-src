package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// A2UI surfaces in the side panel's chat. Both methods are mutating actions of
// the panel's own chat, gated exactly as chat.send is (they exist only while a
// chat backend is installed, need a named profile, and the CLI they run
// enforces the operator rule itself).
//
//	chat.ui_action {profile, conversation, action}
//	    action is the A2UI action envelope {"version":"1.0","action":{...}}.
//	    -> the `chat ui action` document: {outcome, reason?, delivered?,
//	       handled?, message?, agent_id?}
//	chat.ui_sync   {profile, conversation}
//	    -> the `chat ui sync-hil` document: {surfaced:[ids], resolved:[ids]}
//	chat.ui_journal {profile, conversation, seen?:[turn ids]}
//	    -> {turns:[{turn, events:[{seq,type,payload,at}]}], more}
//	    Cards written outside a running turn (sync-hil, a press) are finished
//	    turns of their own: never live, and seq starts at 1 in each. This
//	    returns those not in seen, oldest first. Read only.
const (
	MethodChatUIAction  = "chat.ui_action"
	MethodChatUISync    = "chat.ui_sync"
	MethodChatUIJournal = "chat.ui_journal"
)

const (
	chatTurnWindow     = 200 // turns looked at, newest first (the CLI's page limit)
	chatJournalPerCall = 20
	chatJournalSeenMax = chatTurnWindow
)

// ChatJournalTurn is one journal turn's events.
type ChatJournalTurn struct {
	Turn   string      `json:"turn"`
	Events []ChatEvent `json:"events"`
}

// ChatUIJournalPage is chat.ui_journal's reply; More says another call has more.
type ChatUIJournalPage struct {
	Turns []ChatJournalTurn `json:"turns"`
	More  bool              `json:"more"`
}

// isJournalTurn tells the turns `chat ui action` and `chat ui sync-hil` write
// (ai.RecordUIAction, ai.RecordUISurfaces) from the conversation's own.
func isJournalTurn(id string) bool {
	return strings.HasPrefix(id, "system-uiaction-") || strings.HasPrefix(id, "system-uisurface-")
}

// ChatUIBackend is the optional half of a ChatBackend that runs the surface
// commands. The two methods are advertised only when the backend has it.
type ChatUIBackend interface {
	// UIAction journals the person's press; action is the envelope's JSON.
	UIAction(ctx context.Context, profile, conversation string, action []byte) (json.RawMessage, error)
	// UISync surfaces pending approvals of the conversation and resolves the answered ones.
	UISync(ctx context.Context, profile, conversation string) (json.RawMessage, error)
	// UIJournal returns the journal turns not in seen, oldest first.
	UIJournal(ctx context.Context, profile, conversation string, seen map[string]bool) (ChatUIJournalPage, error)
}

func (cb *chatBridge) uiJournal(b ChatUIBackend) RequestHandler {
	return func(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
		profile, err := requireProfile(req)
		if err != nil {
			return nil, err
		}
		conv, err := chatConversation(req, true)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		if raw := req.Params["seen"]; raw != nil {
			list, ok := raw.([]any)
			if !ok || len(list) > chatJournalSeenMax {
				return nil, invalidParam("seen must be a list of at most %d turn ids", chatJournalSeenMax)
			}
			for _, v := range list {
				id, ok := v.(string)
				if !ok || !chatIDPattern.MatchString(id) {
					return nil, invalidParam("seen holds an invalid turn id")
				}
				seen[id] = true
			}
		}
		return b.UIJournal(ctx, profile, conv, seen)
	}
}

func (cb *chatBridge) uiAction(b ChatUIBackend) RequestHandler {
	return func(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
		profile, err := requireProfile(req)
		if err != nil {
			return nil, err
		}
		conv, err := chatConversation(req, true)
		if err != nil {
			return nil, err
		}
		env, ok := req.Params["action"].(map[string]any)
		if !ok {
			return nil, invalidParam("action must be an A2UI action envelope")
		}
		raw, err := json.Marshal(env)
		if err != nil || len(raw) > a2ui.DefaultLimits.MaxActionBytes {
			return nil, invalidParam("action is larger than %d bytes", a2ui.DefaultLimits.MaxActionBytes)
		}
		if _, err := a2ui.ParseAction(raw, a2ui.DefaultLimits); err != nil {
			return nil, invalidParam("%v", err)
		}
		return b.UIAction(ctx, profile, conv, raw)
	}
}

func (cb *chatBridge) uiSync(b ChatUIBackend) RequestHandler {
	return func(ctx context.Context, req *Request, _ ProgressFunc) (any, error) {
		profile, err := requireProfile(req)
		if err != nil {
			return nil, err
		}
		conv, err := chatConversation(req, true)
		if err != nil {
			return nil, err
		}
		return b.UISync(ctx, profile, conv)
	}
}

// uiActionArgv reads the envelope from stdin: it carries the person's edits, which must not show in a process
// listing.
func uiActionArgv(profile, conversation string) []string {
	return append(chatBase(profile), "chat", "ui", "action", "--action", "-", "--", conversation)
}

func uiSyncArgv(profile, conversation string) []string {
	return append(chatBase(profile), "chat", "ui", "sync-hil", "--", conversation)
}

func (b *cliChatBackend) uiRun(ctx context.Context, stdin []byte, argv []string) (json.RawMessage, error) {
	out, err := b.runStdin(ctx, stdin, argv...)
	if err != nil {
		return nil, err
	}
	return uiDocument(out)
}

// uiDocument is the one JSON value a `chat ui` command printed.
func uiDocument(out []byte) (json.RawMessage, error) {
	// The CLI indents its document over many lines: the whole of stdout is the one value.
	var doc bytes.Buffer
	var probe map[string]any
	if json.Unmarshal(out, &probe) != nil || probe == nil || json.Compact(&doc, out) != nil {
		return nil, errors.New("chat ui returned unreadable output")
	}
	return json.RawMessage(doc.Bytes()), nil
}

// uiPressWait is how long a press of a host card is waited for before the
// panel is told it is still in progress. Approving a send performs it, which
// can take minutes.
var uiPressWait = 12 * time.Second

// pressInProgress answers such a press: the command goes on, and the card it
// journals says how far it is.
const pressInProgress = `{"outcome":"accepted","delivered":"system","inProgress":true}`

// UIAction runs the press on its own (clicore.RunSurviving): neither the
// request's deadline nor a panel that closes ends a send half way.
func (b *cliChatBackend) UIAction(ctx context.Context, profile, conversation string, action []byte) (json.RawMessage, error) {
	bin, err := b.binary()
	if err != nil {
		return nil, err
	}
	press, err := a2ui.ParseAction(action, a2ui.DefaultLimits)
	host := err == nil && strings.HasPrefix(press.Name, "hil.")
	wait := ctx
	if host {
		var cancel context.CancelFunc
		wait, cancel = context.WithTimeout(ctx, uiPressWait)
		defer cancel()
	}
	cmd := exec.Command(bin, uiActionArgv(profile, conversation)...) //nolint:gosec // our own CLI
	out, stderr, err := clicore.RunSurviving(wait, cmd, action)
	switch {
	case errors.Is(err, clicore.ErrStillRunning) && host:
		return json.RawMessage(pressInProgress), nil
	case errors.Is(err, clicore.ErrStillRunning):
		return nil, &RequestError{Code: CodeTimeout, Err: err}
	case err != nil:
		return nil, cliFailure(out, string(stderr), err)
	}
	return uiDocument(out)
}

func (b *cliChatBackend) UISync(ctx context.Context, profile, conversation string) (json.RawMessage, error) {
	return b.uiRun(ctx, nil, uiSyncArgv(profile, conversation))
}

// turnIDs lists the conversation's own turns, newest first.
func (b *cliChatBackend) turnIDs(ctx context.Context, profile, conversation string) ([]string, error) {
	out, err := b.run(ctx, append(chatBase(profile), "chat", "history", "turns",
		"--limit="+strconv.Itoa(chatTurnWindow), "--", conversation)...)
	if err != nil {
		return nil, err
	}
	var turns struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &turns) != nil {
		return nil, errors.New("chat history turns returned unreadable output")
	}
	ids := make([]string, 0, len(turns.Items))
	for _, t := range turns.Items {
		if chatIDPattern.MatchString(t.ID) {
			ids = append(ids, t.ID)
		}
	}
	return ids, nil
}

// UIJournal reads the card turns with their events in one CLI run, whatever
// their number, and hands out the oldest ones not seen.
func (b *cliChatBackend) UIJournal(ctx context.Context, profile, conversation string, seen map[string]bool) (ChatUIJournalPage, error) {
	page := ChatUIJournalPage{Turns: []ChatJournalTurn{}}
	args := append(chatBase(profile), "chat", "history", "turns", "--limit="+strconv.Itoa(chatTurnWindow), "--cards", "--events")
	if len(seen) > 0 {
		// The ids passed chatIDPattern: none holds a comma.
		skip := make([]string, 0, len(seen))
		for id := range seen {
			skip = append(skip, id)
		}
		sort.Strings(skip)
		args = append(args, "--skip="+strings.Join(skip, ","))
	}
	out, err := b.run(ctx, append(args, "--", conversation)...)
	if err != nil {
		return page, err
	}
	var cards struct {
		Items []struct {
			ID     string              `json:"id"`
			Events []chatevents.Record `json:"events"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &cards) != nil {
		return page, errors.New("chat history turns returned unreadable output")
	}
	for i := len(cards.Items) - 1; i >= 0; i-- { // oldest first
		it := cards.Items[i]
		if !chatIDPattern.MatchString(it.ID) || !isJournalTurn(it.ID) || seen[it.ID] {
			continue
		}
		turn := ChatJournalTurn{Turn: it.ID, Events: make([]ChatEvent, 0, len(it.Events))}
		for _, r := range it.Events {
			turn.Events = append(turn.Events, ChatEvent{Seq: r.Seq, Type: string(r.Type), Payload: r.Payload, At: r.At})
		}
		page.Turns = append(page.Turns, turn)
	}
	// Turns made in the same second list in id order: the events say which came first.
	sort.SliceStable(page.Turns, func(i, j int) bool { return firstAt(page.Turns[i]).Before(firstAt(page.Turns[j])) })
	if len(page.Turns) > chatJournalPerCall {
		page.Turns, page.More = page.Turns[:chatJournalPerCall], true
	}
	return page, nil
}

func firstAt(t ChatJournalTurn) time.Time {
	if len(t.Events) == 0 {
		return time.Time{}
	}
	at, _ := time.Parse(time.RFC3339Nano, t.Events[0].At)
	return at
}
