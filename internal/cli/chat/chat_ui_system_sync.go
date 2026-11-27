package chat

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/a2ui"
	"github.com/monoes/mono-agent/internal/a2ui/surfaces"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/hilsend"
)

// SyncHILResult is what `chat ui sync-hil` prints. Surfaced and Resolved are
// surface ids ("hil-<id>", "send-<id>"). Skipped lists pending items that got
// no surface and why (too large to show whole, or the conversation already
// holds as many live surfaces as it may); they stay on the queue, and a later
// sync tries them again.
type SyncHILResult struct {
	Surfaced []string      `json:"surfaced"`
	Resolved []string      `json:"resolved"`
	Skipped  []SyncSkipped `json:"skipped,omitempty"`
}

// SyncSkipped is one pending item that was not surfaced.
type SyncSkipped struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func newChatUISyncHILCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "sync-hil <conversation-id> --profile <profile>",
		Short: "Show the profile's pending approvals as surfaces in a conversation",
		Long: "Lists the profile's pending Human-in-Loop items and send approvals (what `hil list --json` lists) and journals one " +
			"system surface into the conversation for each one it has not surfaced yet (surface id hil-<id> or send-<id>), and " +
			"a notice in place of each surface whose item is no longer pending. Running it again surfaces nothing new. " +
			"Prints {\"surfaced\":[surface ids],\"resolved\":[surface ids]} (plus \"skipped\" when an item could not be shown) " +
			"and exits 0. A conversation with no turn of its own yet gets no surface.",
		Example: `  monoagentcli chat ui sync-hil c1 --profile work`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := runSyncHIL(cmd.Context(), cfg, args[0])
			if err != nil {
				return err
			}
			return clicore.PrintJSON(res)
		},
	}
}

func runSyncHIL(ctx context.Context, cfg *Deps, conversationID string) (*SyncHILResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	store, profileID, closeDB, err := openChatHistory(cfg)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	if profileID == "" {
		return nil, clicore.InvalidInput("name the profile with --profile")
	}
	if _, err := store.GetConversation(conversationID, profileID); err != nil {
		return nil, chatStoreErr(err)
	}
	// A card written here is a turn of its own, and any turn locks the chat's mode and marks the conversation as
	// started. So a conversation with no work turn yet gets none; a later sync shows its cards.
	started, err := store.HasWorkTurn(profileID, conversationID)
	if err != nil {
		return nil, err
	}
	if !started {
		return &SyncHILResult{Surfaced: []string{}, Resolved: []string{}}, nil
	}
	evs, err := store.UIEvents(profileID, conversationID)
	if err != nil {
		return nil, err
	}
	replay := dynorg.RebuildRegistry(evs)
	e := &sysEnv{cfg: cfg}
	listed, err := e.listHIL(ctx)
	if err != nil {
		return nil, err
	}
	items := waitingItems(listed)
	res := &SyncHILResult{Surfaced: []string{}, Resolved: []string{}}
	var payloads []chatevents.UISurfacePayload
	add := func(ref chatevents.UIRef, msgs [][]byte) bool {
		for _, raw := range msgs {
			if _, err := replay.Registry.Handle(raw, a2ui.FromSystem); err != nil {
				return false
			}
			m, _ := a2ui.ParseMessage(raw, a2ui.DefaultLimits)
			r := ref
			payloads = append(payloads, chatevents.UISurfacePayload{SurfaceID: m.SurfaceID, Provenance: string(a2ui.FromSystem), Ref: &r, Message: raw})
		}
		return true
	}

	pending := map[string]bool{}
	for _, it := range items {
		pending[surfaceOfItem(it)] = true
	}
	for _, id := range liveCardIDs(replay) {
		if ref := refOfSurface(evs, id); !pending[id] && (ref.State == refSending || ref.State == refSendingElsewhere) {
			// An approved send's note stays until the item says how it ended.
			state, sendError := e.sendState(ctx, ref.ID)
			if state == "" || state == hilsend.StateApproved || state == hilsend.StatePending {
				continue
			}
			note, tone := outcomeNote(true, state, sendError)
			if state == hilsend.StateSent {
				note, tone = "Sent", "success"
			}
			if ref.State == refSendingElsewhere && state != hilsend.StateSent {
				note, tone = elsewhereNote(state, sendError)
			}
			ref.State = ""
			if msgs, err := surfaces.ResolvedNotice(id, e.redact(ctx, note), tone); err == nil {
				// The note replaces a note: delete and create apply as one.
				if add(ref, msgs) {
					res.Resolved = append(res.Resolved, id)
				}
			}
			continue
		}
		if pending[id] || !hasButton(replay.Registry.Surface(id)) {
			continue
		}
		if msgs, err := surfaces.ResolvedNotice(id, "No longer pending", "neutral"); err == nil && add(refOfSurface(evs, id), msgs) {
			res.Resolved = append(res.Resolved, id)
		}
	}
	for _, it := range items {
		id := surfaceOfItem(it)
		old := replay.Registry.Surface(id)
		live := old != nil && old.Prov == a2ui.FromSystem && hasButton(old)
		if live && !it.Redacted && refOfSurface(evs, id).Digest == it.Digest {
			continue
		}
		if old != nil && !live && startingSend(evs, id) {
			continue
		}
		// Its card was never drawn, is gone, stands as a note although the item
		// still waits, or shows the item as it no longer is: draw it (again).
		ref, msgs, err := buildItemSurface(it)
		if err == nil && old != nil {
			var del [][]byte
			if del, err = surfaces.Resolved(id); err == nil {
				msgs = append(del, msgs...)
			}
		}
		if err != nil && live {
			// The old card must not stay: it offers to approve what is not there.
			if note, nerr := surfaces.ResolvedNotice(id, unshowable, "warning"); nerr == nil {
				add(ref, note)
			}
		}
		if err == nil && !add(ref, msgs) {
			err = errors.New("the conversation holds as many live surfaces as it may")
		}
		if err != nil {
			res.Skipped = append(res.Skipped, SyncSkipped{ID: it.ID, Reason: err.Error()})
			continue
		}
		res.Surfaced = append(res.Surfaced, id)
	}
	if _, err := store.RecordUISurfaces(profileID, conversationID, payloads); err != nil {
		return nil, err
	}
	return res, nil
}

// sendingGrace is how long an in-progress note may stand while its item still
// reads pending: the moment between the note and the command's claim.
var sendingGrace = time.Minute

// startingSend says a send's in-progress note was written so recently that its
// command may not have claimed the item yet.
func startingSend(evs []chatevents.Event, surfaceID string) bool {
	var at string
	var ref chatevents.UIRef
	for _, ev := range evs {
		var p chatevents.UISurfacePayload
		if ev.Type != chatevents.EventUISurface || json.Unmarshal(ev.Payload, &p) != nil {
			continue
		}
		if p.SurfaceID == surfaceID && p.Provenance == string(a2ui.FromSystem) && p.Ref != nil {
			at, ref = ev.At, *p.Ref
		}
	}
	written, err := time.Parse(time.RFC3339Nano, at)
	return ref.State == refSending && err == nil && time.Since(written) < sendingGrace
}

func surfaceOfItem(it listedHIL) string {
	if it.Kind == "send" && it.Send != nil {
		return surfaces.SendSurfaceID(it.Send.ID)
	}
	return surfaces.HILSurfaceID(it.ID)
}

// waitingItems keeps what waits for a decision, once per card. A listed send
// that is already decided would get a card without buttons, and a second entry
// for one card would differ from it: either would be drawn again at every run,
// growing the journal for as long as the chat is open.
func waitingItems(items []listedHIL) []listedHIL {
	seen := map[string]bool{}
	return slices.DeleteFunc(items, func(it listedHIL) bool {
		id := surfaceOfItem(it)
		drop := seen[id] || (it.Kind == "send" && it.Send != nil && it.Send.State != hilsend.StatePending)
		seen[id] = true
		return drop
	})
}

// buildItemSurface is the card of a pending item and the ref that ties it to
// the item as shown. The ref is set even when no card can be built.
func buildItemSurface(it listedHIL) (chatevents.UIRef, [][]byte, error) {
	if it.Redacted {
		kind := surfaces.RefHIL
		if it.Kind == "send" {
			kind = surfaces.RefSend
		}
		return chatevents.UIRef{Kind: kind, ID: it.ID, Digest: it.Digest}, nil,
			errors.New("it holds a reply link that chat does not show: decide it in Communications")
	}
	if it.Kind == "send" && it.Send != nil {
		msgs, err := surfaces.Send(*it.Send, it.Digest)
		return chatevents.UIRef{Kind: surfaces.RefSend, ID: it.Send.ID, Digest: it.Digest}, msgs, err
	}
	msgs, err := surfaces.HIL(it.HILItem)
	return chatevents.UIRef{Kind: surfaces.RefHIL, ID: it.ID, Digest: it.Digest}, msgs, err
}

// liveCardIDs are the live hil-/send- surfaces of the replay, sorted.
func liveCardIDs(r *dynorg.UIReplay) []string {
	var ids []string
	for id := range r.Authors {
		if len(id) > 4 && (id[:4] == "hil-" || (len(id) > 5 && id[:5] == "send-")) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func hasButton(s *a2ui.Surface) bool {
	if s == nil {
		return false
	}
	for _, c := range s.Components {
		if c["component"] == "Button" {
			return true
		}
	}
	return false
}
