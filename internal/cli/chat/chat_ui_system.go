package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The handlers of the system surfaces (hil., system., mission.). Each one
// takes the press, checks it again against the record the surface was built
// from (surfaces.Route: surface id, context id, fields), then does what the
// button says by running the same CLI command a person would, in process
// (Deps.RunCLI), never its own SQL. On success it replaces the surface with a
// button-less notice; on failure it returns the error, which `chat ui action`
// journals as a rejected press, and leaves the surface live.

// uiConv is what `chat ui action` tells a system handler about the press.
type uiConv struct {
	ID, Profile string
	// out is set by a handler that wants the caller to send a next user
	// message (mission.edit): `chat ui action` returns it as "message".
	out *uiOut
}

type uiOut struct{ Message string }

type uiConvKey struct{}

func withUIConv(ctx context.Context, conversationID, profileID string) context.Context {
	return context.WithValue(ctx, uiConvKey{}, uiConv{ID: conversationID, Profile: profileID, out: &uiOut{}})
}

func uiConvOf(ctx context.Context) (uiConv, bool) {
	c, ok := ctx.Value(uiConvKey{}).(uiConv)
	return c, ok && c.ID != ""
}

// registerSystemHandlers sets the handlers of the three reserved prefixes.
func registerSystemHandlers(cfg *Deps, sys *SystemActions) {
	e := &sysEnv{cfg: cfg}
	_ = sys.Register("hil.", e.hil)
	_ = sys.Register("system.", e.answer)
	_ = sys.Register("mission.", e.mission)
}

type sysEnv struct{ cfg *Deps }

// route re-checks the press against the record its surface was built from.
func (e *sysEnv) route(ctx context.Context, a a2ui.Action) (surfaces.Command, chatevents.UIRef, uiConv, error) {
	conv, ok := uiConvOf(ctx)
	if !ok {
		return surfaces.Command{}, chatevents.UIRef{}, conv, errors.New("a system action runs only through `chat ui action`")
	}
	store, _, closeDB, err := openChatHistory(e.cfg)
	if err != nil {
		return surfaces.Command{}, chatevents.UIRef{}, conv, err
	}
	defer closeDB()
	evs, err := store.UIEvents(conv.Profile, conv.ID)
	if err != nil {
		return surfaces.Command{}, chatevents.UIRef{}, conv, err
	}
	ref := refOfSurface(evs, a.SurfaceID)
	if ref.Kind == "" {
		return surfaces.Command{}, ref, conv, fmt.Errorf("surface %q has no record behind it", a.SurfaceID)
	}
	cmd, err := surfaces.Route(a, ref)
	return cmd, ref, conv, err
}

// refOfSurface is the ref of the newest system ui.surface event of a surface.
func refOfSurface(evs []chatevents.Event, surfaceID string) chatevents.UIRef {
	var ref chatevents.UIRef
	for _, ev := range evs {
		var p chatevents.UISurfacePayload
		if ev.Type != chatevents.EventUISurface || json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		if p.SurfaceID == surfaceID && p.Provenance == string(a2ui.FromSystem) && p.Ref != nil {
			ref = *p.Ref
		}
	}
	return ref
}

// recordMessages journals system messages of one record as surface events.
func recordMessages(store *ai.AIStore, conv uiConv, ref chatevents.UIRef, msgs [][]byte) error {
	ps := make([]chatevents.UISurfacePayload, 0, len(msgs))
	for _, raw := range msgs {
		m, err := a2ui.ParseMessage(raw, a2ui.DefaultLimits)
		if err != nil {
			return err
		}
		r := ref
		ps = append(ps, chatevents.UISurfacePayload{SurfaceID: m.SurfaceID, Provenance: string(a2ui.FromSystem), Ref: &r, Message: raw})
	}
	_, err := store.RecordUISurfaces(conv.Profile, conv.ID, ps)
	return err
}

// redact hides reply link tokens in text that goes into the journal.
func (e *sysEnv) redact(ctx context.Context, s string) string {
	db, err := e.cfg.OpenDB()
	if err != nil {
		return "(details withheld)"
	}
	defer db.Close()
	return workflow.NewReplyRedactor(ctx, db.DB).String(s)
}

// finish replaces the surface with a notice. It is best effort: the decision
// is already made, and `chat ui sync-hil` settles a card this missed.
func (e *sysEnv) finish(conv uiConv, ref chatevents.UIRef, surfaceID, text, tone string) {
	msgs, err := surfaces.ResolvedNotice(surfaceID, e.redact(context.Background(), text), tone)
	if err != nil {
		return
	}
	store, _, closeDB, err := openChatHistory(e.cfg)
	if err != nil {
		return
	}
	defer closeDB()
	_ = recordMessages(store, conv, ref, msgs)
}

func (e *sysEnv) run(ctx context.Context, args ...string) ([]byte, error) {
	if e.cfg.RunCLI == nil {
		return nil, errors.New("this build cannot run the command a system button stands for")
	}
	return e.cfg.RunCLI(ctx, args...)
}

// hil runs hil.approve and hil.reject: `hil approve|reject <id>`. A press is
// accepted only when the item ends in the state its button names; otherwise the
// card shows what really happened and the press is journaled rejected.
func (e *sysEnv) hil(ctx context.Context, a a2ui.Action, _ *a2ui.Surface) error {
	cmd, ref, conv, err := e.route(ctx, a)
	if err != nil {
		return err
	}
	surfaceID, approve := a.SurfaceID, cmd.Kind == "hil.approve"
	if ref.Digest == "" {
		return errors.New("this card does not say what it showed: wait for it to be drawn again")
	}
	args := []string{"hil", "reject", cmd.ID}
	if approve {
		data, err := e.approveData(ctx, ref, cmd)
		if err != nil {
			return err
		}
		args = []string{"hil", "approve", cmd.ID, "--data", data}
	}
	// The command decides the item only while it is what the card showed.
	out, err := e.run(ctx, append(args, "--if-digest", ref.Digest)...)
	if err != nil {
		// The card stays live while the item still waits, drawn anew if it changed.
		switch it, lerr := e.pendingItem(ctx, cmd.ID); {
		case errors.Is(lerr, errNotPending):
			e.finish(conv, ref, surfaceID, "No longer pending", "neutral")
		case lerr == nil && it.Digest != ref.Digest:
			e.redraw(conv, surfaceID, it)
		}
		return errors.New(e.redact(ctx, err.Error()))
	}
	// A send prints its card ({state, error}), anything else {status}.
	var res struct {
		Status string `json:"status"`
		State  string `json:"state"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(out, &res)
	state := hilsend.State(firstSet(res.State, res.Status))
	switch {
	case !approve && state == hilsend.StateRejected:
		e.finish(conv, ref, surfaceID, "Rejected by you", "neutral")
		return nil
	case approve && (state == hilsend.StateSent || (state == hilsend.StateApproved && ref.Kind != surfaces.RefSend)):
		e.finish(conv, ref, surfaceID, "Approved by you", "success")
		return nil
	case approve && state == hilsend.StateApproved:
		e.finish(conv, ref, surfaceID, "Approved, being sent now", "info")
		return nil
	}
	note, tone := outcomeNote(approve, state, res.Error)
	if state != hilsend.StatePending {
		e.finish(conv, ref, surfaceID, note, tone)
	}
	return errors.New(e.redact(ctx, note))
}

// unshowable is the note that takes the place of a card whose item changed into
// one no card can show whole.
const unshowable = "This item changed and cannot be shown whole here: decide it in Communications"

// redraw replaces a card with one of the item as it is now, best effort (sync
// does the same at its next run).
func (e *sysEnv) redraw(conv uiConv, surfaceID string, it listedHIL) {
	ref, msgs, err := buildItemSurface(it)
	if err != nil {
		e.finish(conv, ref, surfaceID, unshowable, "warning")
		return
	}
	del, err := surfaces.Resolved(surfaceID)
	if err != nil {
		return
	}
	store, _, closeDB, err := openChatHistory(e.cfg)
	if err != nil {
		return
	}
	defer closeDB()
	_ = recordMessages(store, conv, ref, append(del, msgs...))
}

// outcomeNote says what really happened to an item whose state is not the one
// the pressed button names.
func outcomeNote(approve bool, state hilsend.State, sendError string) (text, tone string) {
	switch {
	case approve && state == hilsend.StateFailed:
		return "Approved by you, but sending failed: " + sendError, "danger"
	case approve && state == hilsend.StateRejected:
		return "Not sent: this send was rejected", "neutral"
	case !approve && state == hilsend.StateSent:
		return "Not rejected: it was already sent", "warning"
	case !approve && state == hilsend.StateApproved:
		return "Not rejected: it was already approved and is being sent", "warning"
	case !approve && state == hilsend.StateFailed:
		return "Not rejected: it was approved and sending failed: " + sendError, "warning"
	case state == hilsend.StatePending:
		return "The item is still waiting: nothing was decided", "warning"
	}
	return "No longer pending", "neutral"
}

func firstSet(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// approveData is the --data of the approval: the edited fields, as the item
// holds them (a number stays a number, a boolean a boolean). A field the
// item does not have is refused.
func (e *sysEnv) approveData(ctx context.Context, ref chatevents.UIRef, cmd surfaces.Command) (string, error) {
	edited := map[string]any{}
	if ref.Kind == surfaces.RefSend {
		for k, v := range cmd.Fields {
			if k != "text" && k != "subject" {
				return "", fmt.Errorf("a send takes only text and subject, not %q", k)
			}
			edited[k] = v
		}
	} else {
		item, err := e.pendingItem(ctx, cmd.ID)
		if err != nil {
			return "", err
		}
		for k, v := range cmd.Fields {
			orig, has := item.EditableData[k]
			if !has {
				return "", fmt.Errorf("the item has no editable field %q", k)
			}
			edited[k] = typedLike(orig, v)
		}
	}
	b, err := json.Marshal(edited)
	return string(b), err
}

// typedLike converts the text of an edited field back to the type of the value it replaces.
func typedLike(orig any, s string) any {
	switch orig.(type) {
	case float64:
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return f
		}
	case bool:
		if b, err := strconv.ParseBool(strings.TrimSpace(s)); err == nil {
			return b
		}
	}
	return s
}

type listedHIL struct {
	surfaces.HILItem
	Digest   string         `json:"digest"`
	Redacted bool           `json:"redacted"`
	Kind   string           `json:"kind"`
	Send   *hilsend.Payload `json:"send"`
}

// listHIL is `hil list --json --redact`: cards, and the journal that keeps
// them, hold only what an agent may read.
func (e *sysEnv) listHIL(ctx context.Context) ([]listedHIL, error) {
	out, err := e.run(ctx, "hil", "list", "--redact")
	if err != nil {
		return nil, err
	}
	var items []listedHIL
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("hil list: %w", err)
	}
	return items, nil
}

func (e *sysEnv) pendingItem(ctx context.Context, id string) (listedHIL, error) {
	items, err := e.listHIL(ctx)
	if err != nil {
		return listedHIL{}, err
	}
	for _, it := range items {
		if it.ID == id {
			return it, nil
		}
	}
	return listedHIL{}, fmt.Errorf("HIL item %q is %w", id, errNotPending)
}

var errNotPending = errors.New("not pending any more")
