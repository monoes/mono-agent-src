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

// finish replaces the surface with a notice. It is best effort: the decision
// is already made, and `chat ui sync-hil` settles a card this missed.
func (e *sysEnv) finish(conv uiConv, ref chatevents.UIRef, surfaceID, text, tone string) {
	msgs, err := surfaces.ResolvedNotice(surfaceID, text, tone)
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

// hil runs hil.approve and hil.reject: `hil approve|reject <id>`.
func (e *sysEnv) hil(ctx context.Context, a a2ui.Action, _ *a2ui.Surface) error {
	cmd, ref, conv, err := e.route(ctx, a)
	if err != nil {
		return err
	}
	surfaceID := a.SurfaceID
	if cmd.Kind == "hil.reject" {
		if _, err := e.run(ctx, "hil", "reject", cmd.ID); err != nil {
			return err
		}
		e.finish(conv, ref, surfaceID, "Rejected by you", "neutral")
		return nil
	}
	data, err := e.approveData(ctx, ref, cmd)
	if err != nil {
		return err
	}
	out, err := e.run(ctx, "hil", "approve", cmd.ID, "--data", data)
	if err != nil {
		return err
	}
	var res struct {
		Status string `json:"status"`
		State  string `json:"state"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(out, &res)
	if res.State == string(hilsend.StateFailed) {
		e.finish(conv, ref, surfaceID, "Approved by you, but sending failed: "+res.Error, "danger")
		return fmt.Errorf("the send failed: %s", res.Error)
	}
	e.finish(conv, ref, surfaceID, "Approved by you", "success")
	return nil
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
	Kind string           `json:"kind"`
	Send *hilsend.Payload `json:"send"`
}

// listHIL is `hil list --json`.
func (e *sysEnv) listHIL(ctx context.Context) ([]listedHIL, error) {
	out, err := e.run(ctx, "hil", "list")
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
	return listedHIL{}, fmt.Errorf("HIL item %q is not pending any more", id)
}
